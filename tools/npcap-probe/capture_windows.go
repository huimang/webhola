//go:build windows && amd64

package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// Windows uses 32-bit C long (including timeval) even on amd64.
type packetHeader struct {
	Sec, Usec      int32
	Caplen, Length uint32
}
type pcapDevice struct {
	Next              *pcapDevice
	Name, Description *byte
	Addresses         unsafe.Pointer
	Flags             uint32
}
type bpfProgram struct {
	Length       uint32
	Instructions unsafe.Pointer
}
type captureStats struct{ Received, Dropped, InterfaceDropped, Captured uint32 }
type captureDevice struct {
	ID, Description string
	Loopback        bool
}

type npcap struct {
	dll                                                                                                   *syscall.DLL
	find, freeDevices, open, close, next, datalink, compile, setfilter, freecode, nonblock, geterr, stats *syscall.Proc
}

func cstring(p *byte) string {
	if p == nil {
		return ""
	}
	var b []byte
	for i := 0; i < 4096; i++ {
		v := *(*byte)(unsafe.Add(unsafe.Pointer(p), i))
		if v == 0 {
			break
		}
		b = append(b, v)
	}
	return string(b)
}

func loadNpcap(path string) (*npcap, error) {
	kernel := syscall.NewLazyDLL("kernel32.dll")
	if path == "" {
		var directory [32768]uint16
		n, _, err := kernel.NewProc("GetSystemDirectoryW").Call(uintptr(unsafe.Pointer(&directory[0])), uintptr(len(directory)))
		if n == 0 || n >= uintptr(len(directory)) {
			return nil, fmt.Errorf("system directory: %w", err)
		}
		path = filepath.Join(syscall.UTF16ToString(directory[:n]), "Npcap", "wpcap.dll")
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("-dll must be an absolute path")
	}
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	// Search dependencies only beside the explicitly selected DLL and in System32.
	h, _, err := kernel.NewProc("LoadLibraryExW").Call(uintptr(unsafe.Pointer(p)), 0, 0x100|0x800)
	runtime.KeepAlive(p)
	if h == 0 {
		return nil, fmt.Errorf("load Npcap %s: %w; Npcap must be installed", path, err)
	}
	d := &npcap{dll: &syscall.DLL{Name: path, Handle: syscall.Handle(h)}}
	for _, e := range []struct {
		name string
		dst  **syscall.Proc
	}{
		{"pcap_findalldevs", &d.find}, {"pcap_freealldevs", &d.freeDevices}, {"pcap_open_live", &d.open},
		{"pcap_close", &d.close}, {"pcap_next_ex", &d.next}, {"pcap_datalink", &d.datalink},
		{"pcap_compile", &d.compile}, {"pcap_setfilter", &d.setfilter}, {"pcap_freecode", &d.freecode},
		{"pcap_setnonblock", &d.nonblock}, {"pcap_geterr", &d.geterr}, {"pcap_stats", &d.stats},
	} {
		*e.dst, err = d.dll.FindProc(e.name)
		if err != nil {
			d.dll.Release()
			return nil, err
		}
	}
	if init, err := d.dll.FindProc("pcap_init"); err == nil {
		var msg [256]byte
		rc, _, _ := init.Call(1, uintptr(unsafe.Pointer(&msg[0])))
		if int32(rc) != 0 {
			d.dll.Release()
			return nil, fmt.Errorf("pcap_init: %s", cstring(&msg[0]))
		}
	}
	return d, nil
}

func (d *npcap) devices() ([]captureDevice, error) {
	var head *pcapDevice
	var msg [256]byte
	rc, _, _ := d.find.Call(uintptr(unsafe.Pointer(&head)), uintptr(unsafe.Pointer(&msg[0])))
	if int32(rc) != 0 {
		return nil, fmt.Errorf("pcap_findalldevs: %s", cstring(&msg[0]))
	}
	defer d.freeDevices.Call(uintptr(unsafe.Pointer(head)))
	var out []captureDevice
	for p := head; p != nil; p = p.Next {
		out = append(out, captureDevice{ID: cstring(p.Name), Description: cstring(p.Description), Loopback: p.Flags&1 != 0})
	}
	return out, nil
}

type npcapSource struct {
	api    *npcap
	handle uintptr
	link   int
	device string
}

var _ PacketSource = (*npcapSource)(nil)

func (d *npcap) openSource(device string, port int) (*npcapSource, error) {
	var msg [256]byte
	name, err := syscall.BytePtrFromString(device)
	if err != nil {
		return nil, err
	}
	h, _, _ := d.open.Call(uintptr(unsafe.Pointer(name)), 262144, 0, 100, uintptr(unsafe.Pointer(&msg[0])))
	runtime.KeepAlive(name)
	if h == 0 {
		return nil, fmt.Errorf("pcap_open_live: %s (check Npcap capture permissions)", cstring(&msg[0]))
	}
	s := &npcapSource{api: d, handle: h, device: device}
	fail := func(err error) (*npcapSource, error) { s.Close(); return nil, err }
	link, _, _ := d.datalink.Call(h)
	s.link = int(int32(link))
	if !supportedLink(s.link) {
		return fail(fmt.Errorf("unsupported capture link type %d", s.link))
	}
	filter, _ := syscall.BytePtrFromString(fmt.Sprintf("tcp port %d", port))
	var program bpfProgram
	rc, _, _ := d.compile.Call(h, uintptr(unsafe.Pointer(&program)), uintptr(unsafe.Pointer(filter)), 1, 0xffffffff)
	runtime.KeepAlive(filter)
	if int32(rc) != 0 {
		return fail(s.lastError("pcap_compile"))
	}
	rc, _, _ = d.setfilter.Call(h, uintptr(unsafe.Pointer(&program)))
	d.freecode.Call(uintptr(unsafe.Pointer(&program)))
	if int32(rc) != 0 {
		return fail(s.lastError("pcap_setfilter"))
	}
	rc, _, _ = d.nonblock.Call(h, 1, uintptr(unsafe.Pointer(&msg[0])))
	if int32(rc) != 0 {
		return fail(fmt.Errorf("pcap_setnonblock: %s", cstring(&msg[0])))
	}
	return s, nil
}

func (s *npcapSource) lastError(op string) error {
	p, _, _ := s.api.geterr.Call(s.handle)
	// pcap_geterr returns a C-owned pointer, not an address into the Go heap.
	ptr := *(*unsafe.Pointer)(unsafe.Pointer(&p))
	return fmt.Errorf("%s: %s", op, cstring((*byte)(ptr)))
}

func (s *npcapSource) Close() error {
	if s.handle != 0 {
		s.api.close.Call(s.handle)
		s.handle = 0
	}
	return nil
}

func (s *npcapSource) ReadPacket(ctx context.Context) (IPPacket, error) {
	for {
		if err := ctx.Err(); err != nil {
			return IPPacket{}, err
		}
		var header *packetHeader
		var data *byte
		rc, _, _ := s.api.next.Call(s.handle, uintptr(unsafe.Pointer(&header)), uintptr(unsafe.Pointer(&data)))
		switch int32(rc) {
		case 0:
			select {
			case <-ctx.Done():
				return IPPacket{}, ctx.Err()
			case <-time.After(10 * time.Millisecond):
			}
		case 1:
			if header == nil || data == nil || header.Caplen > 262144 || header.Caplen != header.Length {
				return IPPacket{}, fmt.Errorf("invalid or truncated capture")
			}
			raw := append([]byte(nil), unsafe.Slice(data, int(header.Caplen))...)
			ip, err := normalizeIP(raw, s.link)
			if err != nil {
				return IPPacket{}, err
			}
			return IPPacket{Data: ip, Timestamp: time.Unix(int64(header.Sec), int64(header.Usec)*1000), DeviceID: s.device}, nil
		default:
			return IPPacket{}, s.lastError("pcap_next_ex")
		}
	}
}

func capture(ctx context.Context, o options) error {
	d, err := loadNpcap(o.dll)
	if err != nil {
		return err
	}
	defer d.dll.Release()
	devices, err := d.devices()
	if err != nil {
		return err
	}
	fmt.Println("Npcap capture devices (pcap_findalldevs):")
	for _, dev := range devices {
		fmt.Printf("  device=%s loopback=%t description=%q\n", dev.ID, dev.Loopback, dev.Description)
	}
	if o.list {
		return nil
	}
	chosen := o.device
	if o.selftest || o.loopback {
		for _, dev := range devices {
			if dev.Loopback || strings.EqualFold(dev.ID, `\Device\NPF_Loopback`) {
				chosen = dev.ID
				break
			}
		}
		if chosen == "" {
			return fmt.Errorf("Npcap loopback capture device not found")
		}
	}
	if chosen == "" {
		return fmt.Errorf("select -device from -list or specify -loopback")
	}
	found := false
	for _, dev := range devices {
		if dev.ID == chosen {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("capture device not found: %s", chosen)
	}
	var result chan error
	var send func() error
	if o.selftest {
		port, request, cleanup, err := prepareSelfTest(o.port)
		if err != nil {
			return err
		}
		defer cleanup()
		o.port = port
		send = request
		result = make(chan error, 1)
	}
	source, err := d.openSource(chosen, o.port)
	if err != nil {
		return err
	}
	defer source.Close()
	fmt.Printf("CAPTURE ready: device=%q linkType=%d filter=%q\n", chosen, source.link, fmt.Sprintf("tcp port %d", o.port))
	fmt.Println("Direct Npcap capture; packet payload previews only, no TCP/HTTP reassembly or HTTPS decryption.")
	if o.selftest {
		go func() { result <- send() }()
	}
	var packets, payloadBytes int
	var requestSeen, responseSeen, clientDone bool
	var clientErr error
	var reader PacketSource = source
	for {
		pkt, err := reader.ReadPacket(ctx)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			return err
		}
		summary, payload, err := describeTCP(pkt.Data)
		if err != nil {
			return err
		}
		packets++
		payloadBytes += len(payload)
		if strings.Contains(string(payload), "GET /npcap-probe") {
			requestSeen = true
		}
		if strings.Contains(string(payload), "npcap-probe-ok") {
			responseSeen = true
		}
		fmt.Printf("#%d time=%s ipBytes=%d %s\n", packets, pkt.Timestamp.Format(time.RFC3339Nano), len(pkt.Data), summary)
		if len(payload) > 0 {
			preview := payload
			if len(preview) > 180 {
				preview = preview[:180]
			}
			fmt.Printf("  payload[%d]=%q\n", len(payload), preview)
		}
		if o.count > 0 && packets >= o.count {
			break
		}
		if o.selftest {
			if !clientDone {
				select {
				case clientErr = <-result:
					clientDone = true
				default:
				}
			}
			if clientDone && (clientErr != nil || requestSeen && responseSeen) {
				break
			}
		}
	}
	var stats captureStats
	rc, _, _ := d.stats.Call(source.handle, uintptr(unsafe.Pointer(&stats)))
	if int32(rc) == 0 {
		fmt.Printf("SUMMARY packets=%d tcpPayloadBytes=%d pcapReceived=%d pcapDropped=%d interfaceDropped=%d\n", packets, payloadBytes, stats.Received, stats.Dropped, stats.InterfaceDropped)
	} else {
		fmt.Printf("SUMMARY packets=%d tcpPayloadBytes=%d drops=unknown\n", packets, payloadBytes)
	}
	if o.selftest {
		if !clientDone {
			select {
			case clientErr = <-result:
				clientDone = true
			case <-time.After(4 * time.Second):
				return fmt.Errorf("HTTP client did not finish")
			}
		}
		if clientErr != nil {
			return fmt.Errorf("HTTP self-test client: %w", clientErr)
		}
		if !requestSeen || !responseSeen {
			return fmt.Errorf("self-test incomplete: requestPacket=%t responseBodyPacket=%t", requestSeen, responseSeen)
		}
		fmt.Println("SELF-TEST PASS: captured HTTP request and response body; HTTP round trip succeeded without TShark.")
	}
	return nil
}

func prepareSelfTest(port int) (int, func() error, func(), error) {
	l, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return 0, nil, nil, err
	}
	server := &http.Server{ReadHeaderTimeout: 2 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "npcap-probe-ok")
	})}
	go server.Serve(l)
	actual := l.Addr().(*net.TCPAddr).Port
	send := func() error {
		transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
		res, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/npcap-probe", actual))
		if err != nil {
			return err
		}
		defer res.Body.Close()
		body, err := io.ReadAll(io.LimitReader(res.Body, 1024))
		if err != nil {
			return err
		}
		if res.StatusCode != 200 || string(body) != "npcap-probe-ok" {
			return fmt.Errorf("unexpected HTTP result: %s %q", res.Status, body)
		}
		return nil
	}
	return actual, send, func() { server.Close() }, nil
}
