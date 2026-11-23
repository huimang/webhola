package capture

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	"github.com/gopacket/gopacket/pcapgo"
)

type sliceSource struct {
	packets []IPPacket
	next    int
	closed  bool
}

func (s *sliceSource) ReadPacket(ctx context.Context) (IPPacket, error) {
	if err := ctx.Err(); err != nil {
		return IPPacket{}, err
	}
	if s.next >= len(s.packets) {
		return IPPacket{}, io.EOF
	}
	p := s.packets[s.next]
	s.next++
	return p, nil
}
func (s *sliceSource) Stats() (CaptureStats, error) { return CaptureStats{}, nil }
func (s *sliceSource) Close() error                 { s.closed = true; return nil }

type tcpFlags uint8

const (
	flagFIN tcpFlags = 1 << iota
	flagSYN
	flagRST
	flagPSH
	flagACK
)

func buildIPPacket(ipv6 bool, src, dst string, sport, dport uint16, seq, ack uint32, flags tcpFlags, payload []byte) []byte {
	tcp := &layers.TCP{
		SrcPort: layers.TCPPort(sport), DstPort: layers.TCPPort(dport),
		Seq: seq, Ack: ack, Window: 65535,
		SYN: flags&flagSYN != 0, ACK: flags&flagACK != 0, FIN: flags&flagFIN != 0,
		RST: flags&flagRST != 0, PSH: flags&flagPSH != 0,
	}
	buf := gopacket.NewSerializeBuffer()
	opts := gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}
	var err error
	if ipv6 {
		ip := &layers.IPv6{Version: 6, HopLimit: 64, NextHeader: layers.IPProtocolTCP, SrcIP: net.ParseIP(src), DstIP: net.ParseIP(dst)}
		tcp.SetNetworkLayerForChecksum(ip)
		err = gopacket.SerializeLayers(buf, opts, ip, tcp, gopacket.Payload(payload))
	} else {
		ip := &layers.IPv4{Version: 4, TTL: 64, Protocol: layers.IPProtocolTCP, SrcIP: net.ParseIP(src).To4(), DstIP: net.ParseIP(dst).To4()}
		tcp.SetNetworkLayerForChecksum(ip)
		err = gopacket.SerializeLayers(buf, opts, ip, tcp, gopacket.Payload(payload))
	}
	if err != nil {
		panic(err)
	}
	return append([]byte(nil), buf.Bytes()...)
}

type testConn struct {
	ipv6                   bool
	clientIP, serverIP     string
	clientPort, serverPort uint16
	cseq, sseq             uint32
	ts                     time.Time
	packets                []IPPacket
}

func newTestConn() *testConn {
	return &testConn{clientIP: "127.0.0.1", serverIP: "127.0.0.1", clientPort: 50000, serverPort: 8080, cseq: 1000, sseq: 5000, ts: time.Unix(1700000000, 0)}
}

func (c *testConn) emit(fromClient bool, seq, ack uint32, flags tcpFlags, payload []byte) {
	srcIP, dstIP := c.clientIP, c.serverIP
	sport, dport := c.clientPort, c.serverPort
	if !fromClient {
		srcIP, dstIP = dstIP, srcIP
		sport, dport = dport, sport
	}
	raw := buildIPPacket(c.ipv6, srcIP, dstIP, sport, dport, seq, ack, flags, payload)
	c.ts = c.ts.Add(time.Millisecond)
	c.packets = append(c.packets, IPPacket{Data: raw, Timestamp: c.ts, InterfaceID: "test"})
}

func (c *testConn) handshake() {
	c.emit(true, c.cseq, 0, flagSYN, nil)
	c.cseq++
	c.emit(false, c.sseq, c.cseq, flagSYN|flagACK, nil)
	c.sseq++
	c.emit(true, c.cseq, c.sseq, flagACK, nil)
}
func (c *testConn) client(payload string) {
	c.emit(true, c.cseq, c.sseq, flagPSH|flagACK, []byte(payload))
	c.cseq += uint32(len(payload))
}
func (c *testConn) server(payload string) {
	c.emit(false, c.sseq, c.cseq, flagPSH|flagACK, []byte(payload))
	c.sseq += uint32(len(payload))
}
func (c *testConn) reset() { c.emit(false, c.sseq, c.cseq, flagRST|flagACK, nil) }

// serverSegments splits payload into normal TCP segments, as a large body
// arrives in practice.
func (c *testConn) serverSegments(payload []byte) {
	for len(payload) > 0 {
		n := 1400
		if len(payload) < n {
			n = len(payload)
		}
		c.server(string(payload[:n]))
		payload = payload[n:]
	}
}

func runPipeline(t *testing.T, packets []IPPacket) ([]Exchange, Diagnostics) {
	t.Helper()
	source := &sliceSource{packets: packets}
	var mu sync.Mutex
	var got []Exchange
	diag, err := Process(context.Background(), source, CaptureConfig{TCPPort: 8080}, func(e Exchange) {
		mu.Lock()
		got = append(got, e)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	return got, diag
}

func finalExchanges(all []Exchange) []Exchange {
	var out []Exchange
	for _, e := range all {
		if e.Response != nil {
			out = append(out, e)
		}
	}
	return out
}

func TestPreferredAdapter(t *testing.T) {
	cases := []struct {
		name string
		in   CaptureInterface
		want bool
	}{
		{"wlan", CaptureInterface{ID: `\Device\NPF_{WIFI}`, Name: "Intel(R) Wi-Fi 6 AX201 160MHz"}, true},
		{"ethernet", CaptureInterface{ID: `\Device\NPF_{ETH}`, Name: "Realtek PCIe GbE Family Controller"}, true},
		{"loopback", CaptureInterface{ID: `\Device\NPF_Loopback`, Name: "Adapter for loopback traffic capture", IsLoopback: true}, true},
		{"linux-eth", CaptureInterface{ID: "eth0", Name: "eth0"}, true},
		{"vpn", CaptureInterface{ID: `\Device\NPF_{VPN}`, Name: "Sangfor SSL VPN CS Support System VNIC"}, false},
		{"tap", CaptureInterface{ID: `\Device\NPF_{TAP}`, Name: "TAP-Windows Adapter V9"}, false},
		{"hyperv", CaptureInterface{ID: `\Device\NPF_{WSL}`, Name: "Hyper-V Virtual Ethernet Adapter"}, false},
		{"bluetooth", CaptureInterface{ID: `\Device\NPF_{BT}`, Name: "Bluetooth Device (Personal Area Network)"}, false},
		{"wan-miniport", CaptureInterface{ID: `\Device\NPF_{WAN}`, Name: "WAN Miniport (IP)"}, false},
		{"wireguard", CaptureInterface{ID: "wg0", Name: "wg0"}, false},
		{"utun", CaptureInterface{ID: "utun3", Name: "utun3"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := preferredAdapter(tc.in); got != tc.want {
				t.Fatalf("preferredAdapter(%+v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestOversizedIPv4LengthIsRepaired(t *testing.T) {
	c := newTestConn()
	c.handshake()
	c.client("GET /repair HTTP/1.1\r\nHost: example.test:8080\r\n\r\n")
	c.server("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")

	// Simulate a Wi-Fi offload frame whose IPv4 total length includes the
	// link-layer header, which makes gopacket reject it as truncated.
	last := len(c.packets) - 1
	ip := c.packets[last].Data
	binary.BigEndian.PutUint16(ip[2:4], 0)

	var logs []string
	var mu sync.Mutex
	got, diag := runPipelineLogged(t, c, func(format string, args ...any) {
		mu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		mu.Unlock()
	})
	if diag.InvalidIP != 0 {
		t.Fatalf("invalid packets = %d, want 0 (logs=%v)", diag.InvalidIP, logs)
	}
	if finals := finalExchanges(got); len(finals) != 1 || finals[0].Response == nil {
		t.Fatalf("exchanges = %+v diag=%+v logs=%v", got, diag, logs)
	}
}

func runPipelineLogged(t *testing.T, c *testConn, logf func(string, ...any)) ([]Exchange, Diagnostics) {
	t.Helper()
	var mu sync.Mutex
	var got []Exchange
	diag, err := Process(context.Background(), &sliceSource{packets: c.packets}, CaptureConfig{TCPPort: 8080, Logf: logf}, func(e Exchange) {
		mu.Lock()
		got = append(got, e)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	return got, diag
}

func TestLargeChunkedGzipResponse(t *testing.T) {
	body := []byte(`{"items":[` + strings.Repeat(`{"id":123,"name":"training record","ok":true},`, 50000) + `{}]}`)
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	var chunked bytes.Buffer
	for rest := compressed.Bytes(); len(rest) > 0; {
		n := 4096
		if len(rest) < n {
			n = len(rest)
		}
		fmt.Fprintf(&chunked, "%x\r\n", n)
		chunked.Write(rest[:n])
		chunked.WriteString("\r\n")
		rest = rest[n:]
	}
	chunked.WriteString("0\r\n\r\n")

	c := newTestConn()
	c.handshake()
	c.client("GET /large HTTP/1.1\r\nHost: example.test:8080\r\nAccept-Encoding: gzip\r\n\r\n")
	c.serverSegments([]byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Encoding: gzip\r\nTransfer-Encoding: chunked\r\n\r\n" + chunked.String()))

	got, diag := runPipeline(t, c.packets)
	finals := finalExchanges(got)
	if len(finals) != 1 {
		t.Fatalf("final exchanges = %d, want 1 (all=%d diag=%+v)", len(finals), len(got), diag)
	}
	e := finals[0]
	if e.Status != 200 || e.Response == nil {
		t.Fatalf("response = %+v", e)
	}
	if e.Response.Bytes != len(body) || !e.Response.Truncated || len(e.Response.RawBody) != MaxBody {
		t.Fatalf("bytes=%d truncated=%v raw=%d, want %d/%v/%d", e.Response.Bytes, e.Response.Truncated, len(e.Response.RawBody), len(body), true, MaxBody)
	}
}

func TestTCPSequenceJumpDiagnostic(t *testing.T) {
	c := newTestConn()
	c.handshake()
	c.client("GET /gap HTTP/1.1\r\nHost: example.test:8080\r\n\r\n")
	c.server("HTTP/1.1 200 OK\r\nContent-Length: 20\r\n\r\nabc")
	// The next segment starts 100 bytes after the expected one: a capture loss
	// or reordering the reassembler cannot report on its own.
	c.emit(false, c.sseq+100, c.cseq, flagPSH|flagACK, []byte("defghij"))
	var mu sync.Mutex
	var logs []string
	_, _ = Process(context.Background(), &sliceSource{packets: c.packets}, CaptureConfig{TCPPort: 8080, Logf: func(format string, args ...any) {
		mu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		mu.Unlock()
	}}, func(Exchange) {})
	mu.Lock()
	defer mu.Unlock()
	for _, line := range logs {
		if strings.Contains(line, "tcp sequence jump") {
			return
		}
	}
	t.Fatalf("no sequence jump logged: %v", logs)
}

func TestLargeResponse(t *testing.T) {
	body := strings.Repeat("x", 4<<20)
	c := newTestConn()
	c.handshake()
	c.client("GET /large HTTP/1.1\r\nHost: example.test:8080\r\n\r\n")
	c.serverSegments([]byte(fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: %d\r\n\r\n%s", len(body), body)))

	got, diag := runPipeline(t, c.packets)
	finals := finalExchanges(got)
	if len(finals) != 1 {
		t.Fatalf("final exchanges = %d, want 1 (all=%d diag=%+v)", len(finals), len(got), diag)
	}
	if e := finals[0]; e.Response == nil || e.Response.Bytes != len(body) || !e.Response.Truncated {
		t.Fatalf("response = %+v", e.Response)
	}
}

func TestPipelineRequestResponse(t *testing.T) {
	c := newTestConn()
	c.handshake()
	c.client("GET /hello?x=1 HTTP/1.1\r\nHost: example.test:8080\r\nUser-Agent: probe\r\nContent-Length: 0\r\n\r\n")
	c.server("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 5\r\n\r\nhello")

	got, _ := runPipeline(t, c.packets)
	finals := finalExchanges(got)
	if len(finals) != 1 {
		t.Fatalf("final exchanges = %d, want 1 (all=%d)", len(finals), len(got))
	}
	e := finals[0]
	if e.Method != "GET" || e.URL != "http://example.test:8080/hello?x=1" {
		t.Fatalf("request = %s %s", e.Method, e.URL)
	}
	if e.Status != 200 || e.Response.Body != "hello" || e.Response.Bytes != 5 {
		t.Fatalf("response = %d %q bytes=%d", e.Status, e.Response.Body, e.Response.Bytes)
	}
	if e.Request.Line != "GET /hello?x=1 HTTP/1.1" || !strings.Contains(e.Request.Headers, "Host: example.test:8080") {
		t.Fatalf("request line/headers = %q / %q", e.Request.Line, e.Request.Headers)
	}
	if e.Client != "127.0.0.1:50000" || e.Server != "127.0.0.1:8080" {
		t.Fatalf("endpoints = %s -> %s", e.Client, e.Server)
	}
	if e.Stream != "1" {
		t.Fatalf("stream = %q, want short id", e.Stream)
	}
	if e.Duration <= 0 {
		t.Fatalf("duration = %v", e.Duration)
	}
}

func TestPipelineKeepAlive(t *testing.T) {
	c := newTestConn()
	c.handshake()
	c.client("GET /one HTTP/1.1\r\nHost: h\r\n\r\n")
	c.server("HTTP/1.1 200 OK\r\nContent-Length: 3\r\n\r\none")
	c.client("POST /two HTTP/1.1\r\nHost: h\r\nContent-Length: 3\r\n\r\ntwo")
	c.server("HTTP/1.1 201 Created\r\nContent-Length: 2\r\n\r\nok")

	finals := finalExchanges(func() []Exchange { g, _ := runPipeline(t, c.packets); return g }())
	if len(finals) != 2 {
		t.Fatalf("final exchanges = %d, want 2", len(finals))
	}
	if finals[0].URL != "http://h/one" || finals[0].Status != 200 {
		t.Fatalf("first = %s %d", finals[0].URL, finals[0].Status)
	}
	if finals[1].URL != "http://h/two" || finals[1].Status != 201 || finals[1].Request.Body != "two" {
		t.Fatalf("second = %s %d %q", finals[1].URL, finals[1].Status, finals[1].Request.Body)
	}
}

func TestPipelineOutOfOrderAndRetransmit(t *testing.T) {
	c := newTestConn()
	c.handshake()
	request := "GET /reordered HTTP/1.1\r\nHost: h\r\n\r\n"
	part := len(request) / 3
	segments := []string{request[:part], request[part : 2*part], request[2*part:]}
	for _, i := range []int{1, 0, 2, 1} {
		seq := c.cseq + uint32(i*part)
		c.emit(true, seq, c.sseq, flagPSH|flagACK, []byte(segments[i]))
	}
	c.cseq += uint32(len(request))
	c.server("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")

	finals := finalExchanges(func() []Exchange { g, _ := runPipeline(t, c.packets); return g }())
	if len(finals) != 1 || finals[0].URL != "http://h/reordered" {
		t.Fatalf("finals = %+v", finals)
	}
}

func TestPipelineChunkedGzip(t *testing.T) {
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write([]byte("hello gzip")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	chunk := fmt.Sprintf("%x\r\n", len(compressed.Bytes())) + compressed.String() + "\r\n0\r\n\r\n"

	c := newTestConn()
	c.handshake()
	c.client("GET /gzip HTTP/1.1\r\nHost: h\r\n\r\n")
	c.server("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Encoding: gzip\r\nTransfer-Encoding: chunked\r\n\r\n" + chunk)

	finals := finalExchanges(func() []Exchange { g, _ := runPipeline(t, c.packets); return g }())
	if len(finals) != 1 {
		t.Fatalf("finals = %d", len(finals))
	}
	if finals[0].Response.Body != "hello gzip" || finals[0].Response.Bytes != len("hello gzip") {
		t.Fatalf("body = %q bytes=%d", finals[0].Response.Body, finals[0].Response.Bytes)
	}
}

func TestPipelineInterimResponse(t *testing.T) {
	c := newTestConn()
	c.handshake()
	c.client("POST /upload HTTP/1.1\r\nHost: h\r\nExpect: 100-continue\r\nContent-Length: 4\r\n\r\nbody")
	c.server("HTTP/1.1 100 Continue\r\n\r\n")
	c.server("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")

	finals := finalExchanges(func() []Exchange { g, _ := runPipeline(t, c.packets); return g }())
	if len(finals) != 1 {
		t.Fatalf("finals = %d", len(finals))
	}
	if finals[0].Status != 200 || len(finals[0].Interim) != 1 || finals[0].Interim[0] != 100 {
		t.Fatalf("status=%d interim=%v", finals[0].Status, finals[0].Interim)
	}
}

func TestPipelineHeadResponseKeepsBoundary(t *testing.T) {
	c := newTestConn()
	c.handshake()
	c.client("HEAD /head HTTP/1.1\r\nHost: h\r\n\r\n")
	c.server("HTTP/1.1 200 OK\r\nContent-Length: 3\r\n\r\n")
	c.client("GET /next HTTP/1.1\r\nHost: h\r\n\r\n")
	c.server("HTTP/1.1 200 OK\r\nContent-Length: 4\r\n\r\nnext")

	finals := finalExchanges(func() []Exchange { g, _ := runPipeline(t, c.packets); return g }())
	if len(finals) != 2 {
		t.Fatalf("finals = %d, want 2", len(finals))
	}
	if finals[0].Response.Body != "" || finals[1].Response.Body != "next" {
		t.Fatalf("bodies = %q / %q", finals[0].Response.Body, finals[1].Response.Body)
	}
}

func TestPipelinePreviewTruncationConsumesBody(t *testing.T) {
	c := newTestConn()
	c.handshake()
	body := strings.Repeat("x", 70000)
	header := fmt.Sprintf("POST /big HTTP/1.1\r\nHost: h\r\nContent-Length: %d\r\n\r\n", len(body))
	payload := header + body
	for off := 0; off < len(payload); off += 16000 {
		end := off + 16000
		if end > len(payload) {
			end = len(payload)
		}
		c.emit(true, c.cseq+uint32(off), c.sseq, flagPSH|flagACK, []byte(payload[off:end]))
	}
	c.cseq += uint32(len(payload))
	c.server("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
	c.client("GET /after HTTP/1.1\r\nHost: h\r\n\r\n")
	c.server("HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nafter")

	finals := finalExchanges(func() []Exchange { g, _ := runPipeline(t, c.packets); return g }())
	if len(finals) != 2 {
		t.Fatalf("finals = %d, want 2", len(finals))
	}
	if finals[0].Request.Bytes != 70000 || !finals[0].Request.Truncated || len(finals[0].Request.RawBody) != MaxBody {
		t.Fatalf("request bytes=%d truncated=%v raw=%d", finals[0].Request.Bytes, finals[0].Request.Truncated, len(finals[0].Request.RawBody))
	}
	if finals[1].URL != "http://h/after" || finals[1].Response.Body != "after" {
		t.Fatalf("next request lost: %+v", finals[1])
	}
}

func TestPipelineResetEndsConnection(t *testing.T) {
	c := newTestConn()
	c.handshake()
	c.client("GET /reset HTTP/1.1\r\nHost: h\r\n\r\n")
	c.reset()

	got, _ := runPipeline(t, c.packets)
	if len(got) == 0 || got[0].URL != "http://h/reset" {
		t.Fatalf("got = %+v", got)
	}
	for _, e := range finalExchanges(got) {
		t.Fatalf("unexpected final response: %+v", e)
	}
}

func TestPipelineTakesOverMidstream(t *testing.T) {
	c := newTestConn()
	c.client("GET /mid HTTP/1.1\r\nHost: h\r\n\r\n")
	c.server("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")

	got, diag := runPipeline(t, c.packets)
	if diag.Midstream != 1 {
		t.Fatalf("midstream diagnostics = %d", diag.Midstream)
	}
	if len(got) == 0 {
		t.Fatal("midstream request was not emitted")
	}
	last := got[len(got)-1]
	if last.URL != "http://h/mid" || last.Response == nil || last.Response.Body != "ok" {
		t.Fatalf("midstream exchange = %+v", last)
	}
}

func TestPipelineResyncsMidstreamMessage(t *testing.T) {
	c := newTestConn()
	c.client("stale-body\r\nGET /next HTTP/1.1\r\nHost: h\r\n\r\n")
	c.server("stale-response\r\nHTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")

	got, diag := runPipeline(t, c.packets)
	if diag.Midstream != 1 {
		t.Fatalf("midstream diagnostics = %d", diag.Midstream)
	}
	if len(got) == 0 {
		t.Fatal("resynced request was not emitted")
	}
	last := got[len(got)-1]
	if last.URL != "http://h/next" || last.Response == nil || last.Response.Body != "ok" {
		t.Fatalf("resynced exchange = %+v", last)
	}
}

func TestPipelineIPv6(t *testing.T) {
	c := newTestConn()
	c.ipv6 = true
	c.clientIP, c.serverIP = "::1", "::1"
	c.handshake()
	c.client("GET /six HTTP/1.1\r\nHost: [::1]:8080\r\n\r\n")
	c.server("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nv6")

	finals := finalExchanges(func() []Exchange { g, _ := runPipeline(t, c.packets); return g }())
	if len(finals) != 1 || finals[0].Status != 200 || finals[0].Response.Body != "v6" {
		t.Fatalf("finals = %+v", finals)
	}
}

func TestPipelineIPv4FragmentsOutOfOrder(t *testing.T) {
	c := newTestConn()
	c.handshake()
	full := buildIPPacket(false, c.clientIP, c.serverIP, c.clientPort, c.serverPort, c.cseq, c.sseq, flagPSH|flagACK, []byte("GET /frag HTTP/1.1\r\nHost: h\r\n\r\n"))
	c.cseq += uint32(len("GET /frag HTTP/1.1\r\nHost: h\r\n\r\n"))
	first, second := splitIPv4Fragment(t, full, 32)
	c.ts = c.ts.Add(time.Millisecond)
	c.packets = append(c.packets, IPPacket{Data: second, Timestamp: c.ts, InterfaceID: "test"})
	c.ts = c.ts.Add(time.Millisecond)
	c.packets = append(c.packets, IPPacket{Data: first, Timestamp: c.ts, InterfaceID: "test"})
	c.server("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")

	finals := finalExchanges(func() []Exchange { g, _ := runPipeline(t, c.packets); return g }())
	if len(finals) != 1 || finals[0].URL != "http://h/frag" {
		t.Fatalf("finals = %+v", finals)
	}
}

func splitIPv4Fragment(t *testing.T, full []byte, split int) ([]byte, []byte) {
	t.Helper()
	header, payload := full[:20], full[20:]
	if split%8 != 0 || split <= 0 || split >= len(payload) {
		t.Fatalf("invalid split %d", split)
	}
	makePart := func(body []byte, offset int, more bool) []byte {
		out := append([]byte(nil), header...)
		binary.BigEndian.PutUint16(out[2:4], uint16(len(out)+len(body)))
		flags := uint16(offset / 8)
		if more {
			flags |= 0x2000
		}
		binary.BigEndian.PutUint16(out[6:8], flags)
		out[10], out[11] = 0, 0
		return append(out, body...)
	}
	return makePart(payload[:split], 0, true), makePart(payload[split:], split, false)
}

func TestNormalizeIPLinkTypes(t *testing.T) {
	ip := buildIPPacket(false, "10.0.0.1", "10.0.0.2", 1234, 80, 1, 1, flagPSH|flagACK, []byte("x"))
	ethernet := func(etherType uint16, extra []byte) []byte {
		frame := append([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}, []byte{byte(etherType >> 8), byte(etherType)}...)
		frame = append(frame, extra...)
		return append(frame, ip...)
	}
	null := append([]byte{2, 0, 0, 0}, ip...)
	loop := append([]byte{0, 0, 0, 2}, ip...)
	cooked := append(make([]byte, 14), append([]byte{0x08, 0x00}, ip...)...)
	cooked2 := append([]byte{0x08, 0x00}, make([]byte, 18)...)
	cooked2 = append(cooked2, ip...)

	cases := []struct {
		name string
		link int
		raw  []byte
	}{
		{"ethernet", 1, ethernet(0x0800, nil)},
		{"vlan", 1, ethernet(0x8100, []byte{0, 1, 0x08, 0x00})},
		{"qinq", 1, ethernet(0x88a8, []byte{0, 1, 0x81, 0x00, 0, 1, 0x08, 0x00})},
		{"null-loopback", 0, null},
		{"bpf-loopback", 108, loop},
		{"linux-cooked", 113, cooked},
		{"linux-cooked-v2", 276, cooked2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeIP(tc.raw, tc.link)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, ip) {
				t.Fatal("payload does not start at the IP header")
			}
		})
	}
	if _, err := normalizeIP(ethernet(0x0806, nil), 1); err == nil {
		t.Fatal("ARP frame accepted")
	}
	if _, err := normalizeIP([]byte{0, 1}, 1); err == nil {
		t.Fatal("short Ethernet frame accepted")
	}
}

func TestFileSourceReadsPCAP(t *testing.T) {
	testFileSource(t, "sample.pcap", writePCAPFile)
}

func TestFileSourceReadsPCAPNG(t *testing.T) {
	testFileSource(t, "sample.pcapng", writePCAPNGFile)
}

func testFileSource(t *testing.T, name string, write func(string, []IPPacket) error) {
	t.Helper()
	c := newTestConn()
	c.handshake()
	c.client("GET /file HTTP/1.1\r\nHost: h\r\n\r\n")
	c.server("HTTP/1.1 200 OK\r\nContent-Length: 4\r\n\r\nfile")

	path := t.TempDir() + "/" + name
	if err := write(path, c.packets); err != nil {
		t.Fatal(err)
	}
	source, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	var mu sync.Mutex
	var got []Exchange
	if _, err := Process(context.Background(), source, CaptureConfig{TCPPort: 8080}, func(e Exchange) {
		mu.Lock()
		got = append(got, e)
		mu.Unlock()
	}); err != nil {
		t.Fatal(err)
	}
	finals := finalExchanges(got)
	if len(finals) != 1 || finals[0].URL != "http://h/file" || finals[0].Response.Body != "file" {
		t.Fatalf("finals = %+v", finals)
	}
}

func ethernetFrame(ip []byte) []byte {
	etherType := byte(0x08)
	next := byte(0x00)
	if len(ip) > 0 && ip[0]>>4 == 6 {
		next = 0xdd
	}
	frame := append([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, etherType, next}, ip...)
	return frame
}

func writePCAPFile(path string, packets []IPPacket) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := pcapgo.NewWriter(file)
	if err := writer.WriteFileHeader(65535, layers.LinkTypeEthernet); err != nil {
		return err
	}
	for _, p := range packets {
		frame := ethernetFrame(p.Data)
		ci := gopacket.CaptureInfo{Timestamp: p.Timestamp, CaptureLength: len(frame), Length: len(frame)}
		if err := writer.WritePacket(ci, frame); err != nil {
			return err
		}
	}
	return nil
}

func writePCAPNGFile(path string, packets []IPPacket) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer, err := pcapgo.NewNgWriter(file, layers.LinkTypeEthernet)
	if err != nil {
		return err
	}
	for _, p := range packets {
		frame := ethernetFrame(p.Data)
		ci := gopacket.CaptureInfo{Timestamp: p.Timestamp, CaptureLength: len(frame), Length: len(frame)}
		if err := writer.WritePacket(ci, frame); err != nil {
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	return file.Close()
}
