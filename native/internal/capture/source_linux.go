//go:build linux

package capture

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

type linuxAdapter struct{}

func NewAdapter() CaptureAdapter { return linuxAdapter{} }
func (linuxAdapter) Interfaces(ctx context.Context) ([]CaptureInterface, error) {
	return systemInterfaces(ctx)
}
func (linuxAdapter) Open(ctx context.Context, cfg CaptureConfig) (PacketSource, error) {
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
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, int(htons(unix.ETH_P_ALL)))
	if err != nil {
		return nil, fmt.Errorf("AF_PACKET (requires root or CAP_NET_RAW): %w", err)
	}
	if err = unix.Bind(fd, &unix.SockaddrLinklayer{Protocol: htons(unix.ETH_P_ALL), Ifindex: i.Index}); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return &linuxSource{fd: fd, intf: *i}, nil
}
func htons(v uint16) uint16 { return v<<8 | v>>8 }

type linuxSource struct {
	mu   sync.Mutex
	fd   int
	intf net.Interface
	sourceCounters
	drops uint64
}

func (s *linuxSource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fd >= 0 {
		unix.Close(s.fd)
		s.fd = -1
	}
	return nil
}
func (s *linuxSource) Stats() (CaptureStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.snapshot()
	if s.fd < 0 {
		return out, ErrClosed
	}
	st, err := unix.GetsockoptTpacketStats(s.fd, unix.SOL_PACKET, unix.PACKET_STATISTICS)
	if err != nil {
		return out, err
	}
	s.drops += uint64(st.Drops)
	out.Dropped = s.drops
	out.DropsKnown = true
	return out, nil
}
func (s *linuxSource) ReadPacket(ctx context.Context) (IPPacket, error) {
	buf := make([]byte, 262144)
	for {
		if err := ctx.Err(); err != nil {
			return IPPacket{}, err
		}
		s.mu.Lock()
		if s.fd < 0 {
			s.mu.Unlock()
			return IPPacket{}, ErrClosed
		}
		_, err := unix.Poll([]unix.PollFd{{Fd: int32(s.fd), Events: unix.POLLIN}}, 20)
		if err != nil {
			s.mu.Unlock()
			if err == unix.EINTR {
				continue
			}
			return IPPacket{}, err
		}
		n, sa, err := unix.Recvfrom(s.fd, buf, unix.MSG_TRUNC)
		stamp := time.Now()
		s.mu.Unlock()
		if err == unix.EAGAIN || err == unix.EINTR {
			continue
		}
		if err != nil {
			return IPPacket{}, err
		}
		ll, ok := sa.(*unix.SockaddrLinklayer)
		if !ok {
			return IPPacket{}, fmt.Errorf("missing AF_PACKET link metadata")
		}
		if ll.Hatype != unix.ARPHRD_ETHER && ll.Hatype != unix.ARPHRD_LOOPBACK {
			return IPPacket{}, fmt.Errorf("unsupported AF_PACKET hardware type %d", ll.Hatype)
		}
		// Loopback reports the same packet twice; keep the incoming copy.
		if s.intf.Flags&net.FlagLoopback != 0 && ll.Pkttype == unix.PACKET_OUTGOING {
			continue
		}
		if n > len(buf) {
			s.invalid.Add(1)
			continue
		}
		ip, err := normalizeIP(buf[:n], 1)
		if err != nil {
			s.invalid.Add(1)
			continue
		}
		s.received.Add(1)
		return IPPacket{Data: append([]byte(nil), ip...), Timestamp: stamp, InterfaceID: s.intf.Name, InterfaceIndex: s.intf.Index}, nil
	}
}
