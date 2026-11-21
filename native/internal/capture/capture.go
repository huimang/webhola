package capture

import (
	"context"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	i18npkg "webhola/native/internal/i18n"
	platformpkg "webhola/native/internal/platform"
)

const MaxBody = 64 * 1024

const MaxEntries = 1000

type Message struct {
	RawBody                          []byte
	Line, Headers, Body, ContentType string
	Bytes                            int
	Truncated                        bool
}

type Exchange struct {
	Replayed                                   bool
	Error                                      string
	ID                                         uint64
	Frame, Stream, Method, URL, Client, Server string
	Started                                    time.Time
	Status                                     int
	Duration                                   time.Duration
	Request                                    Message
	Response                                   *Message
	Interim                                    []int
}

type Field struct {
	Name     string  `xml:"name,attr"`
	Show     string  `xml:"show,attr"`
	Value    string  `xml:"value,attr"`
	Children []Field `xml:"field"`
}

type Protocol struct {
	Name   string  `xml:"name,attr"`
	Fields []Field `xml:"field"`
}

type Packet struct {
	Protocols []Protocol `xml:"proto"`
}

type Values map[string][]Field

func flatten(fs []Field, v Values) {
	for _, f := range fs {
		v[f.Name] = append(v[f.Name], f)
		flatten(f.Children, v)
	}
}

func (v Values) first(key string) string {
	if len(v[key]) > 0 {
		return v[key][0].Show
	}
	return ""
}

func timestamp(v string) time.Time {
	parts := strings.SplitN(v, ".", 2)
	sec, _ := strconv.ParseInt(parts[0], 10, 64)
	var ns int64
	if len(parts) == 2 {
		f := parts[1] + "000000000"
		ns, _ = strconv.ParseInt(f[:9], 10, 64)
	}
	return time.Unix(sec, ns)
}

func decodeText(f Field) string {
	if b, err := hex.DecodeString(f.Value); err == nil && len(b) > 0 {
		return string(b)
	}
	return strings.ReplaceAll(strings.ReplaceAll(f.Show, `\r`, "\r"), `\n`, "\n")
}

func ParseMessage(v Values, response bool) Message {
	kind := "http.request.line"
	if response {
		kind = "http.response.line"
	}
	m := Message{ContentType: v.first("http.content_type")}
	for _, f := range v[kind] {
		m.Headers += decodeText(f)
	}
	// Wireshark's first unnamed field is the actual request/status line.
	for _, f := range v[""] {
		s := decodeText(f)
		if strings.Contains(s, "HTTP/") {
			m.Line = strings.TrimSpace(s)
			break
		}
	}
	if m.Line == "" {
		if response {
			m.Line = strings.TrimSpace(v.first("http.response.version") + " " + v.first("http.response.code") + " " + v.first("http.response.phrase"))
		} else {
			m.Line = strings.TrimSpace(v.first("http.request.method") + " " + v.first("http.request.uri") + " " + v.first("http.request.version"))
		}
	}
	var body []byte
	for _, f := range v["http.file_data"] {
		raw := f.Value
		m.Bytes += len(raw) / 2
		remaining := MaxBody - len(body)
		if remaining > 0 {
			if len(raw) > remaining*2 {
				raw = raw[:remaining*2]
				m.Truncated = true
			}
			b, err := hex.DecodeString(raw)
			if err == nil {
				body = append(body, b...)
			}
		} else {
			m.Truncated = true
		}
	}
	if utf8.Valid(body) && !strings.ContainsRune(string(body), 0) {
		m.Body = string(body)
	} else {
		m.Body = hex.Dump(body)
	}
	m.RawBody = body
	return m
}

// The assembler belongs to the capture worker. UI receives immutable copies.
type assembler struct {
	next    uint64
	Pending map[string][]*Exchange
}

func NewAssembler() *assembler { return &assembler{Pending: make(map[string][]*Exchange)} }

func (a *assembler) Consume(p Packet, emit func(Exchange)) {
	base := Values{}
	for _, pr := range p.Protocols {
		if pr.Name != "http" {
			flatten(pr.Fields, base)
		}
	}
	stream := base.first("tcp.stream")
	src, dst := base.first("ip.src"), base.first("ip.dst")
	if src == "" {
		src = base.first("ipv6.src")
		dst = base.first("ipv6.dst")
	}
	for _, pr := range p.Protocols {
		if pr.Name != "http" {
			continue
		}
		v := Values{}
		flatten(pr.Fields, v)
		if method := v.first("http.request.method"); method != "" {
			a.next++
			url := v.first("http.request.full_uri")
			if url == "" {
				url = "http://" + v.first("http.host") + v.first("http.request.uri")
			}
			x := &Exchange{ID: a.next, Frame: base.first("frame.number"), Stream: stream, Method: method, URL: url, Started: timestamp(base.first("frame.time_epoch")), Client: src + ":" + base.first("tcp.srcport"), Server: dst + ":" + base.first("tcp.dstport"), Request: ParseMessage(v, false)}
			a.Pending[stream] = append(a.Pending[stream], x)
			emit(*x)
			// Bound unmatched traffic as well as visible history.
			if a.next > MaxEntries {
				for s, q := range a.Pending {
					n := 0
					for _, e := range q {
						if e.ID > a.next-MaxEntries {
							q[n] = e
							n++
						}
					}
					if n == 0 {
						delete(a.Pending, s)
					} else {
						a.Pending[s] = q[:n]
					}
				}
			}
		} else if code, _ := strconv.Atoi(v.first("http.response.code")); code > 0 {
			q := a.Pending[stream]
			index := -1
			ref := v.first("http.request_in")
			for i, e := range q {
				if ref == "" || e.Frame == ref {
					index = i
					break
				}
			}
			if index < 0 {
				continue
			}
			x := q[index]
			if code < 200 && code != 101 {
				x.Interim = append(append([]int{}, x.Interim...), code)
				emit(*x)
				continue
			}
			m := ParseMessage(v, true)
			x.Response = &m
			x.Status = code
			x.Duration = timestamp(base.first("frame.time_epoch")).Sub(x.Started)
			emit(*x)
			a.Pending[stream] = append(q[:index], q[index+1:]...)
			if len(a.Pending[stream]) == 0 {
				delete(a.Pending, stream)
			}
		}
	}
}

func ReadPDML(r io.Reader, emit func(Exchange)) error {
	d := xml.NewDecoder(r)
	a := NewAssembler()
	for {
		t, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if s, ok := t.(xml.StartElement); ok && s.Name.Local == "packet" {
			var p Packet
			if err := d.DecodeElement(&p, &s); err != nil {
				return err
			}
			a.Consume(p, emit)
		}
	}
}

func FindTShark() (string, error) {
	if path := os.Getenv("WEBHOLA_TSHARK"); path != "" {
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("WEBHOLA_TSHARK: %w", err)
		}
		return path, nil
	}
	if path, err := exec.LookPath("tshark"); err == nil {
		return path, nil
	}
	for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramW6432")} {
		if root != "" {
			p := filepath.Join(root, "Wireshark", "tshark.exe")
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
		}
	}
	return "", i18npkg.MessageError("capture.tshark_not_found_select_tshark_exe_under")
}

type Device struct{ ID, Label string }

func Devices(ctx context.Context, path string) ([]Device, error) {
	cmd := exec.CommandContext(ctx, path, "-D")
	platformpkg.HideProcess(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, i18npkg.MessageError("capture.unable_to_read_adapters_s_w", strings.TrimSpace(string(out)), err)
	}
	result := ParseDevices(string(out), runtime.GOOS)
	if len(result) == 0 {
		return nil, i18npkg.MessageError("capture.no_local_capture_adapters_available_remote_and")
	}
	return result, nil
}

func ParseDevices(output, goos string) []Device {
	var result []Device
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		parts := strings.SplitN(line, ". ", 2)
		if len(parts) == 2 {
			if n, err := strconv.Atoi(parts[0]); err == nil && n > 0 && isLocalDevice(parts[1], goos) {
				result = append(result, Device{parts[0], parts[1]})
			}
		}
	}
	return result
}

func isLocalDevice(label, goos string) bool {
	name, _, _ := strings.Cut(label, " (")
	name = strings.ToLower(strings.TrimSpace(name))
	if goos == "windows" {
		// Local Npcap adapters (including NPF_Loopback) use this device namespace.
		// Do not classify by description: a real VPN adapter may contain "SSH".
		return strings.HasPrefix(name, `\device\npf_`)
	}
	if strings.HasPrefix(name, "rpcap://") {
		return false
	}
	switch name {
	case "sshdump", "udpdump", "ciscodump", "wifidump", "randpkt", "etwdump", "androiddump", "dpauxmon", "sdjournal":
		return false
	}
	return name != ""
}

func CaptureArgs(iface, port, pcap string) ([]string, error) {
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return nil, i18npkg.MessageError("capture.http_port_must_be_an_integer_between")
	}
	args := []string{"-n", "-l", "-o", "tcp.desegment_tcp_streams:TRUE", "-o", "http.desegment_body:TRUE", "-d", fmt.Sprintf("tcp.port==%d,http", n)}
	if pcap != "" {
		args = append(args, "-r", pcap)
	} else {
		if iface == "" {
			return nil, i18npkg.MessageError("capture.select_a_capture_adapter")
		}
		args = append(args, "-i", iface, "-f", fmt.Sprintf("tcp port %d", n))
	}
	return append(args, "-Y", fmt.Sprintf("http && tcp.port == %d", n), "-T", "pdml"), nil
}

func Capture(ctx context.Context, path string, args []string, ready func(), emit func(Exchange)) error {
	cmd := exec.CommandContext(ctx, path, args...)
	platformpkg.HideProcess(cmd)
	var stderr platformpkg.TailWriter
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	ready()
	parseErr := ReadPDML(out, emit)
	if parseErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return nil
	}
	if waitErr != nil {
		return i18npkg.MessageError("capture.capture_process_exited_s_w", strings.TrimSpace(string(stderr.Buf)), waitErr)
	}
	if parseErr != nil {
		return i18npkg.MessageError("capture.unable_to_read_capture_data_w", parseErr)
	}
	return nil
}
