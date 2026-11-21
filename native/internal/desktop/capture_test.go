package desktop

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	capturepkg "webhola/native/internal/capture"
)

func pf(name, show string) capturepkg.Field { return capturepkg.Field{Name: name, Show: show} }

func TestDeviceListKeepsOnlyLocalAdapters(t *testing.T) {
	const listing = `1. sshdump (SSH remote capture)
2. udpdump (UDP Listener remote capture)
3. randpkt (Random packet generator)
4. ciscodump (Cisco remote capture)
5. etwdump (Event Tracing for Windows reader)
6. wifidump (Wi-Fi remote capture)
7. \Device\NPF_{ABC} (Ethernet)
8. \Device\NPF_Loopback (Adapter for loopback traffic capture)
9. \Device\NPF_{DEF} (SSH VPN adapter)
10. future-extcap (External capture)
invalid line`
	got := capturepkg.ParseDevices(listing, "windows")
	if len(got) != 3 || got[0].ID != "7" || got[1].ID != "8" || got[2].ID != "9" {
		t.Fatalf("local adapter list or original capture IDs lost: %+v", got)
	}
	if got := capturepkg.ParseDevices("1. sshdump (SSH remote capture)\n2. eth0 (Ethernet)\n3. lo (Loopback)\n4. rpcap://host/eth0 (Remote)", "linux"); len(got) != 2 || got[0].ID != "2" || got[1].ID != "3" {
		t.Fatalf("non-Windows local adapters filtered: %+v", got)
	}
}

func packetFor(frame, stream string, fs ...capturepkg.Field) capturepkg.Packet {
	return capturepkg.Packet{Protocols: []capturepkg.Protocol{{Name: "frame", Fields: []capturepkg.Field{pf("frame.number", frame), pf("frame.time_epoch", "1700000000."+frame+"0000000")}}, {Name: "ip", Fields: []capturepkg.Field{pf("ip.src", "127.0.0.1"), pf("ip.dst", "127.0.0.1")}}, {Name: "tcp", Fields: []capturepkg.Field{pf("tcp.stream", stream), pf("tcp.srcport", "50000"), pf("tcp.dstport", "8080")}}, {Name: "http", Fields: fs}}}
}

func requestFields(path string) []capturepkg.Field {
	return []capturepkg.Field{pf("http.request.method", "GET"), pf("http.request.uri", path), pf("http.request.version", "HTTP/1.1"), pf("http.host", "localhost:8080"), {Name: "http.request.line", Value: hex.EncodeToString([]byte("Host: localhost:8080\r\n"))}}
}

func TestAssemblerKeepAliveAndInterim(t *testing.T) {
	a := capturepkg.NewAssembler()
	var got []capturepkg.Exchange
	emit := func(e capturepkg.Exchange) { got = append(got, e) }
	a.Consume(packetFor("1", "0", requestFields("/one")...), emit)
	a.Consume(packetFor("2", "0", requestFields("/two")...), emit)
	a.Consume(packetFor("3", "0", pf("http.response.code", "100"), pf("http.request_in", "1")), emit)
	a.Consume(packetFor("4", "0", pf("http.response.code", "200"), pf("http.request_in", "1"), capturepkg.Field{Name: "http.file_data", Value: "68656c6c6f"}), emit)
	a.Consume(packetFor("5", "0", pf("http.response.code", "404"), pf("http.request_in", "2")), emit)
	if len(got) != 5 || got[2].Response != nil || got[3].ID != 1 || got[4].ID != 2 {
		t.Fatalf("bad pairing: %+v", got)
	}
	if got[3].Response.Body != "hello" || got[3].Request.Headers != "Host: localhost:8080\r\n" || len(got[3].Interim) != 1 {
		t.Fatal("lost payload/headers/interim")
	}
	if got[3].Duration <= 0 || len(a.Pending) != 0 {
		t.Fatal("timing or pending leak")
	}
	if got[0].Response != nil || len(got[0].Interim) != 0 {
		t.Fatal("emitted snapshots were mutated")
	}
}

func TestBodyLimitAndBinary(t *testing.T) {
	m := capturepkg.ParseMessage(capturepkg.Values{"http.file_data": {{Value: strings.Repeat("61", capturepkg.MaxBody+100)}}}, false)
	if !m.Truncated || m.Bytes != capturepkg.MaxBody+100 || len(m.Body) != capturepkg.MaxBody {
		t.Fatal("body bound missing")
	}
	m = capturepkg.ParseMessage(capturepkg.Values{"http.file_data": {{Value: "00ff"}}}, false)
	if !strings.Contains(m.Body, "00 ff") {
		t.Fatal("binary is not hex")
	}
}

func TestXMLMultipleMessagesAndMalformed(t *testing.T) {
	const data = `<?xml version="1.0"?><pdml><packet><proto name="frame"><field name="frame.number" show="1"/><field name="frame.time_epoch" show="1700000000.1"/></proto><proto name="tcp"><field name="tcp.stream" show="0"/></proto><proto name="http"><field name="http.request.method" show="GET"/><field name="http.request.uri" show="/a"/></proto><proto name="http"><field name="http.request.method" show="POST"/><field name="http.request.uri" show="/b"/></proto></packet></pdml>`
	var got []capturepkg.Exchange
	if err := capturepkg.ReadPDML(strings.NewReader(data), func(e capturepkg.Exchange) { got = append(got, e) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Frame != got[1].Frame || got[0].ID == got[1].ID {
		t.Fatal("multiple HTTP messages lost")
	}
	if err := capturepkg.ReadPDML(strings.NewReader(`<pdml><packet>`), func(capturepkg.Exchange) {}); err == nil {
		t.Fatal("malformed XML accepted")
	}
}

func TestCaptureArguments(t *testing.T) {
	for _, port := range []string{"", "0", "65536", "80 or tcp", "-1"} {
		if _, err := capturepkg.CaptureArgs("1", port, ""); err == nil {
			t.Fatalf("invalid port %q accepted", port)
		}
	}
	if _, err := capturepkg.CaptureArgs("", "80", ""); err == nil {
		t.Fatal("empty interface accepted")
	}
	args, err := capturepkg.CaptureArgs("", "8080", "sample.pcap")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(args, "|"), "http && tcp.port == 8080") {
		t.Fatal("offline port filter missing")
	}
}

func TestHistoryClearAndBounds(t *testing.T) {
	a := newApp(context.Background())
	for i := 1; i <= capturepkg.MaxEntries+10; i++ {
		a.accept(capturepkg.Exchange{ID: uint64(i), URL: "/test"})
	}
	if len(a.entries) != capturepkg.MaxEntries || a.discarded != 10 {
		t.Fatal("history is not bounded")
	}
	a.accept(capturepkg.Exchange{ID: 1, Status: 200})
	if len(a.entries) != capturepkg.MaxEntries || a.entries[0].ID != 11 {
		t.Fatal("evicted response resurrected")
	}
	a.clear()
	a.accept(capturepkg.Exchange{ID: 900, Status: 200})
	if len(a.entries) != 0 {
		t.Fatal("cleared request resurrected")
	}
	a.accept(capturepkg.Exchange{ID: 1011, Method: "POST", URL: "/new", Status: 201})
	a.search = "201"
	if a.visibleCount() != 1 {
		t.Fatal("status search failed")
	}
}

func TestPendingBound(t *testing.T) {
	a := capturepkg.NewAssembler()
	for i := 1; i < capturepkg.MaxEntries+30; i++ {
		a.Consume(packetFor(fmt.Sprint(i), fmt.Sprint(i), requestFields("/")...), func(capturepkg.Exchange) {})
	}
	n := 0
	for _, q := range a.Pending {
		n += len(q)
	}
	if n > capturepkg.MaxEntries {
		t.Fatalf("pending grew to %d", n)
	}
}

// A real TCP handshake, split POST body, interim response and split final body.
// No driver or external traffic is needed for this opt-in integration check.
func writeFixturePCAP(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, v := range []any{uint32(0xa1b2c3d4), uint16(2), uint16(4), int32(0), uint32(0), uint32(65535), uint32(1)} {
		if err = binary.Write(f, binary.LittleEndian, v); err != nil {
			return err
		}
	}
	seqC, seqS := uint32(100), uint32(900)
	clock := uint32(0)
	write := func(server bool, flags byte, payload string) error {
		p := make([]byte, 54+len(payload))
		copy(p[:6], []byte{0, 1, 2, 3, 4, 5})
		copy(p[6:12], []byte{6, 7, 8, 9, 10, 11})
		binary.BigEndian.PutUint16(p[12:14], 0x0800)
		ip := p[14:34]
		ip[0] = 0x45
		binary.BigEndian.PutUint16(ip[2:4], uint16(40+len(payload)))
		ip[8] = 64
		ip[9] = 6
		copy(ip[12:16], net.ParseIP("127.0.0.1").To4())
		copy(ip[16:20], ip[12:16])
		tcp := p[34:]
		src, dst := uint16(50000), uint16(8080)
		seq, ack := seqC, seqS
		if server {
			src, dst = dst, src
			seq, ack = seqS, seqC
		}
		binary.BigEndian.PutUint16(tcp[:2], src)
		binary.BigEndian.PutUint16(tcp[2:4], dst)
		binary.BigEndian.PutUint32(tcp[4:8], seq)
		binary.BigEndian.PutUint32(tcp[8:12], ack)
		tcp[12] = 0x50
		tcp[13] = flags
		binary.BigEndian.PutUint16(tcp[14:16], 65535)
		copy(tcp[20:], payload)
		inc := uint32(len(payload))
		if flags&2 != 0 {
			inc++
		}
		if server {
			seqS += inc
		} else {
			seqC += inc
		}
		for _, v := range []uint32{1700000000, clock, uint32(len(p)), uint32(len(p))} {
			if err := binary.Write(f, binary.LittleEndian, v); err != nil {
				return err
			}
		}
		clock += 10000
		_, err := f.Write(p)
		return err
	}
	for _, s := range []struct {
		server bool
		flags  byte
		data   string
	}{{false, 2, ""}, {true, 0x12, ""}, {false, 0x10, ""}, {false, 0x18, "POST /api/test HTTP/1.1\r\nHost: localhost:8080\r\nContent-Type: application/json\r\nContent-Length: 11\r\n\r\n{\"ok\":"}, {false, 0x18, "true}"}, {true, 0x18, "HTTP/1.1 100 Continue\r\n\r\n"}, {true, 0x18, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 16\r\n\r\n{\"message\":"}, {true, 0x18, "\"hi\"}"},
		{false, 0x18, "GET /one HTTP/1.1\r\nHost: localhost:8080\r\n\r\nGET /two HTTP/1.1\r\nHost: localhost:8080\r\n\r\n"},
		{true, 0x18, "HTTP/1.1 200 OK\r\nContent-Length: 3\r\n\r\noneHTTP/1.1 404 Not Found\r\nContent-Length: 3\r\n\r\ntwo"}} {
		if err := write(s.server, s.flags, s.data); err != nil {
			return err
		}
	}
	return nil
}

func TestTSharkIntegration(t *testing.T) {
	if os.Getenv("WEBHOLA_INTEGRATION") != "1" {
		t.Skip("set WEBHOLA_INTEGRATION=1 to run real TShark")
	}
	path, err := capturepkg.FindTShark()
	if err != nil {
		t.Fatal(err)
	}
	pcap := filepath.Join(t.TempDir(), "http.pcap")
	if err = writeFixturePCAP(pcap); err != nil {
		t.Fatal(err)
	}
	args, err := capturepkg.CaptureArgs("", "8080", pcap)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var got []capturepkg.Exchange
	if err = capturepkg.Capture(ctx, path, args, func() {}, func(e capturepkg.Exchange) { got = append(got, e) }); err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 {
		t.Fatalf("missing request/response: %+v", got)
	}
	var e capturepkg.Exchange
	finals := map[string]capturepkg.Exchange{}
	for _, x := range got {
		if x.Response != nil {
			finals[x.URL] = x
		}
		if strings.HasSuffix(x.URL, "/api/test") {
			e = x
		}
	}
	if e.Status != 200 || e.Request.Body != `{"ok":true}` || e.Response == nil || e.Response.Body != `{"message":"hi"}` || !strings.Contains(e.Request.Headers, "Host:") {
		t.Fatalf("bad reassembly: %+v", e)
	}
	if len(finals) != 3 || finals["http://localhost:8080/one"].Status != 200 || finals["http://localhost:8080/two"].Status != 404 {
		t.Fatalf("pipelined messages not paired: %+v", finals)
	}
}
