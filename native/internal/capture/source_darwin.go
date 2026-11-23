//go:build darwin

package capture

import (
	"context"
	"fmt"
	"net"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

type darwinAdapter struct{}

func NewAdapter() CaptureAdapter { return darwinAdapter{} }
func (darwinAdapter) Interfaces(ctx context.Context) ([]CaptureInterface, error) {
	return systemInterfaces(ctx)
}
func (darwinAdapter) Open(ctx context.Context, cfg CaptureConfig) (PacketSource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	i, err := net.InterfaceByName(cfg.InterfaceID)
	if err != nil {
		return nil, err
	}
	if cfg.TCPPort == 0 {
		return nil, fmt.Errorf("invalid capture port")
	}
	fd := -1
	for n := 0; n < 256; n++ {
		fd, err = unix.Open(fmt.Sprintf("/dev/bpf%d", n), unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err == unix.EBUSY {
			continue
		}
		break
	}
	if err != nil {
		return nil, fmt.Errorf("open BPF (requires BPF device permission): %w", err)
	}
	s := &darwinSource{fd: fd, intf: *i}
	fail := func(err error) (PacketSource, error) { s.Close(); return nil, err }
	if err = syscall.SetBpfInterface(fd, i.Name); err != nil {
		return fail(err)
	}
	if err = syscall.SetBpfImmediate(fd, 1); err != nil {
		return fail(err)
	}
	s.link, err = syscall.BpfDatalink(fd)
	if err != nil {
		return fail(err)
	}
	if !supportedLink(s.link) {
		return fail(fmt.Errorf("unsupported BPF link type %d", s.link))
	}
	n, err := syscall.BpfBuflen(fd)
	if err != nil {
		return fail(err)
	}
	if n < 18 || n > 16<<20 {
		return fail(fmt.Errorf("invalid BPF buffer size %d", n))
	}
	s.buffer = make([]byte, n)
	return s, nil
}

type darwinSource struct {
	mu              sync.Mutex
	fd, link        int
	intf            net.Interface
	buffer, pending []byte
	sourceCounters
}

func (s *darwinSource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fd >= 0 {
		unix.Close(s.fd)
		s.fd = -1
	}
	return nil
}
func (s *darwinSource) Stats() (CaptureStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.snapshot()
	if s.fd < 0 {
		return out, ErrClosed
	}
	st, err := syscall.BpfStats(s.fd)
	if err != nil {
		return out, err
	}
	out.Dropped = uint64(st.Drop)
	out.DropsKnown = true
	return out, nil
}
func (s *darwinSource) ReadPacket(ctx context.Context) (IPPacket, error) {
	for {
		if err := ctx.Err(); err != nil {
			return IPPacket{}, err
		}
		s.mu.Lock()
		if s.fd < 0 {
			s.mu.Unlock()
			return IPPacket{}, ErrClosed
		}
		if len(s.pending) == 0 {
			_, err := unix.Poll([]unix.PollFd{{Fd: int32(s.fd), Events: unix.POLLIN}}, 20)
			if err != nil {
				s.mu.Unlock()
				if err == unix.EINTR {
					continue
				}
				return IPPacket{}, err
			}
			n, err := unix.Read(s.fd, s.buffer)
			if err != nil {
				s.mu.Unlock()
				if err == unix.EAGAIN || err == unix.EINTR {
					continue
				}
				return IPPacket{}, err
			}
			s.pending = s.buffer[:n]
		}
		raw, rest, stamp, err := splitBPFRecord(s.pending)
		s.pending = rest
		if err != nil {
			s.invalid.Add(1)
			s.mu.Unlock()
			continue
		}
		ip, err := normalizeIP(raw, s.link)
		if err != nil {
			s.invalid.Add(1)
			s.mu.Unlock()
			continue
		}
		out := IPPacket{Data: append([]byte(nil), ip...), Timestamp: stamp, InterfaceID: s.intf.Name, InterfaceIndex: s.intf.Index}
		s.received.Add(1)
		s.mu.Unlock()
		return out, nil
	}
}
