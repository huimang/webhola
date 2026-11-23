//go:build windows && amd64

package capture

import (
	"context"
	"fmt"
	"unsafe"
)

type windowsAdapter struct{}

func NewAdapter() CaptureAdapter { return windowsAdapter{} }
func (windowsAdapter) Interfaces(ctx context.Context) ([]CaptureInterface, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, err := loadNpcap("")
	if err != nil {
		return nil, err
	}
	defer d.dll.Release()
	devices, err := d.devices()
	if err != nil {
		return nil, err
	}
	out := make([]CaptureInterface, 0, len(devices))
	for _, v := range devices {
		out = append(out, CaptureInterface{ID: v.ID, Name: v.Description, IsLoopback: v.Loopback})
	}
	return out, nil
}
func (windowsAdapter) Open(ctx context.Context, cfg CaptureConfig) (PacketSource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cfg.InterfaceID == "" || cfg.TCPPort == 0 {
		return nil, fmt.Errorf("invalid capture configuration")
	}
	d, err := loadNpcap("")
	if err != nil {
		return nil, err
	}
	// The source owns the loaded library until Close.
	s, err := d.openSource(cfg.InterfaceID, int(cfg.TCPPort), cfg.Logf)
	if err != nil {
		return nil, err
	}
	return s, nil
}
func (s *npcapSource) Stats() (CaptureStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.snapshot()
	if s.handle == 0 {
		return out, ErrClosed
	}
	var stats captureStats
	rc, _, _ := s.api.stats.Call(s.handle, uintptr(unsafe.Pointer(&stats)))
	if int32(rc) != 0 {
		return out, s.lastError("pcap_stats")
	}
	// Npcap's ps_drop counts kernel buffer drops; ps_ifdrop is driver dependent.
	out.Dropped = uint64(stats.Dropped) + uint64(stats.InterfaceDropped)
	out.DropsKnown = true
	return out, nil
}
