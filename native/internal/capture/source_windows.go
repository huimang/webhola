//go:build windows && amd64

package capture

import (
	"context"
	"fmt"
	"sync"

	"path/filepath"
	"runtime"
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
	// setbuff is Npcap's Win32 pcap_setbuff, optional in other libpcap builds.
	setbuff *syscall.Proc
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
	if d.setbuff, err = d.dll.FindProc("pcap_setbuff"); err != nil {
		d.setbuff = nil
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
	mu     sync.Mutex
	done   chan struct{}
	logf   func(string, ...any)
	api    *npcap
	handle uintptr
	link   int
	device string
	sourceCounters
}

var _ PacketSource = (*npcapSource)(nil)

func (d *npcap) openSource(device string, port int, logf func(string, ...any)) (*npcapSource, error) {
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
	s := &npcapSource{api: d, handle: h, device: device, logf: logf, done: make(chan struct{})}
	// The default kernel buffer overflows during large response bursts on
	// Wi-Fi, which leaves TCP reassembly waiting for a lost segment.
	if d.setbuff != nil {
		rc, _, _ := d.setbuff.Call(h, 16<<20)
		if logf != nil {
			logf("npcap buffer 16 MiB: rc=%d", int32(rc))
		}
	} else if logf != nil {
		logf("npcap buffer: pcap_setbuff unavailable")
	}
	fail := func(err error) (*npcapSource, error) { s.Close(); return nil, err }
	link, _, _ := d.datalink.Call(h)
	s.link = int(int32(link))
	if !supportedLink(s.link) {
		return fail(fmt.Errorf("unsupported capture link type %d", s.link))
	}
	// Filter in the kernel: other traffic on a busy Wi-Fi adapter otherwise
	// overflows the capture buffer during large response bursts. Keep IPv4
	// and IPv6 fragments so the decoder can still reassemble them.
	filterText := fmt.Sprintf("(tcp port %d) or (ip[6:2] & 0x1fff != 0) or (ip6[6] == 44)", port)
	filter, _ := syscall.BytePtrFromString(filterText)
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handle != 0 {
		s.api.close.Call(s.handle)
		s.handle = 0
		close(s.done)
		s.api.dll.Release()
	}
	return nil
}

func (s *npcapSource) ReadPacket(ctx context.Context) (IPPacket, error) {
	for {
		if err := ctx.Err(); err != nil {
			return IPPacket{}, err
		}
		s.mu.Lock()
		if s.handle == 0 {
			s.mu.Unlock()
			return IPPacket{}, ErrClosed
		}
		var header *packetHeader
		var data *byte
		rc, _, _ := s.api.next.Call(s.handle, uintptr(unsafe.Pointer(&header)), uintptr(unsafe.Pointer(&data)))
		switch int32(rc) {
		case 0:
			s.mu.Unlock()
			select {
			case <-s.done:
				return IPPacket{}, ErrClosed
			case <-ctx.Done():
				return IPPacket{}, ctx.Err()
			case <-time.After(10 * time.Millisecond):
			}
		case 1:
			if header == nil || data == nil || header.Caplen > 262144 || header.Caplen != header.Length {
				s.invalid.Add(1)
				if s.logf != nil && s.invalid.Load() <= 10 {
					if header == nil {
						s.logf("drop frame: missing header")
					} else {
						s.logf("drop frame: caplen=%d len=%d", header.Caplen, header.Length)
					}
				}
				s.mu.Unlock()
				continue
			}
			raw := append([]byte(nil), unsafe.Slice(data, int(header.Caplen))...)
			stamp := time.Unix(int64(header.Sec), int64(header.Usec)*1000)
			s.mu.Unlock()
			ip, err := normalizeIP(raw, s.link)
			if err != nil {
				s.invalid.Add(1)
				if s.logf != nil && s.invalid.Load() <= 10 {
					s.logf("drop frame len=%d link=%d: %v", len(raw), s.link, err)
				}
				continue
			}
			s.received.Add(1)
			return IPPacket{Data: ip, Timestamp: stamp, InterfaceID: s.device}, nil
		default:
			err := s.lastError("pcap_next_ex")
			s.mu.Unlock()
			return IPPacket{}, err
		}
	}
}
