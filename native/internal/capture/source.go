package capture

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	i18npkg "webhola/native/internal/i18n"
)

var ErrClosed = errors.New("packet source closed")

// IPPacket owns Data, which begins at the IP header (never a capture header).
type IPPacket struct {
	Data           []byte
	Timestamp      time.Time
	InterfaceID    string
	InterfaceIndex int
}

type CaptureInterface struct {
	ID, Name   string
	IsLoopback bool
}

type CaptureConfig struct {
	InterfaceID string
	TCPPort     uint16
	// Logf receives capture diagnostics. nil disables logging.
	Logf func(format string, args ...any)
}

type CaptureStats struct {
	Received, Dropped uint64
	DropsKnown        bool
	Invalid           uint64 // Truncated, malformed or unsupported frames at the adapter.
}

// ReadPacket has one caller; Close and Stats may run concurrently with it.
// Close is idempotent and wakes an idle reader. Received counts delivered IP packets.
type PacketSource interface {
	ReadPacket(context.Context) (IPPacket, error)
	Stats() (CaptureStats, error)
	Close() error
}

type CaptureAdapter interface {
	Interfaces(context.Context) ([]CaptureInterface, error)
	Open(context.Context, CaptureConfig) (PacketSource, error)
}

type sourceCounters struct{ received, invalid atomic.Uint64 }

func (c *sourceCounters) snapshot() CaptureStats {
	return CaptureStats{Received: c.received.Load(), Invalid: c.invalid.Load()}
}

func ListDevices(ctx context.Context) ([]Device, error) {
	list, err := NewAdapter().Interfaces(ctx)
	if err != nil {
		return nil, i18npkg.MessageError("capture.unable_to_enumerate_adapters_w", err)
	}
	preferred := make([]Device, 0, len(list))
	others := make([]Device, 0, len(list))
	for _, d := range list {
		label := d.ID
		if d.Name != "" && d.Name != d.ID {
			label += " (" + d.Name + ")"
		}
		device := Device{ID: d.ID, Label: label, Preferred: preferredAdapter(d)}
		if device.Preferred {
			preferred = append(preferred, device)
		} else {
			others = append(others, device)
		}
	}
	devices := append(preferred, others...)
	if len(devices) == 0 {
		return nil, i18npkg.MessageError("capture.no_local_capture_adapters_available_remote_and")
	}
	return devices, nil
}

// preferredAdapter marks the adapters recommended for local web development.
// VPN, virtual and auxiliary adapters stay selectable but rank below these.
func preferredAdapter(d CaptureInterface) bool {
	if d.IsLoopback {
		return true
	}
	text := strings.ToLower(d.Name + " " + d.ID)
	for _, token := range []string{
		"vpn", "tunnel", "tap-windows", "tap adapter", "wintun", "wireguard",
		"openvpn", "tailscale", "zerotier", "softether", "anyconnect",
		"globalprotect", "pangp", "fortinet", "juniper", "pulse secure",
		"sangfor", "easyconnect", "hamachi", "hyper-v virtual", "bluetooth",
		"wi-fi direct", "virtualbox", "vmware", "kernel debug", "wan miniport",
		"teredo", "isatap", "ras async",
	} {
		if strings.Contains(text, token) {
			return false
		}
	}
	id := strings.ToLower(d.ID)
	for _, prefix := range []string{"tun", "tap", "wg", "utun", "ppp", "ipsec", "gif", "stf"} {
		if strings.HasPrefix(id, prefix) {
			return false
		}
	}
	return true
}

func Config(interfaceID, port, file string) (CaptureConfig, error) {
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 {
		return CaptureConfig{}, i18npkg.MessageError("capture.http_port_must_be_an_integer_between")
	}
	if file == "" && interfaceID == "" {
		return CaptureConfig{}, i18npkg.MessageError("capture.select_a_capture_adapter")
	}
	return CaptureConfig{InterfaceID: interfaceID, TCPPort: uint16(n)}, nil
}

func RunCapture(ctx context.Context, cfg CaptureConfig, file string, ready func(), emit func(Exchange)) error {
	logf := func(format string, args ...any) {
		if cfg.Logf != nil {
			cfg.Logf(format, args...)
		}
	}
	source := "file " + file
	if file == "" {
		source = "adapter " + cfg.InterfaceID
	}
	logf("start %s tcp port %d", source, cfg.TCPPort)
	var packetSource PacketSource
	var err error
	if file != "" {
		packetSource, err = OpenFile(file)
	} else {
		packetSource, err = NewAdapter().Open(ctx, cfg)
	}
	if err != nil {
		logf("open failed: %v", err)
		return err
	}
	defer packetSource.Close()
	if ready != nil {
		ready()
	}
	statsDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		var lastReceived, lastDropped, lastInvalid uint64
		first := true
		for {
			select {
			case <-ctx.Done():
				return
			case <-statsDone:
				return
			case <-ticker.C:
				stats, statErr := packetSource.Stats()
				if statErr != nil {
					continue
				}
				if first || stats.Received != lastReceived || stats.Dropped != lastDropped || stats.Invalid != lastInvalid {
					logf("source received=%d dropped=%d drops_known=%v invalid=%d", stats.Received, stats.Dropped, stats.DropsKnown, stats.Invalid)
					first = false
					lastReceived, lastDropped, lastInvalid = stats.Received, stats.Dropped, stats.Invalid
				}
			}
		}
	}()
	diag, err := Process(ctx, packetSource, cfg, emit)
	close(statsDone)
	if stats, statErr := packetSource.Stats(); statErr == nil {
		logf("source received=%d dropped=%d drops_known=%v invalid=%d", stats.Received, stats.Dropped, stats.DropsKnown, stats.Invalid)
	}
	logf("stop %s tcp port %d: invalid_ip=%d fragments=%d tcp_gaps=%d parse_errors=%d resource_drops=%d midstream=%d",
		source, cfg.TCPPort, diag.InvalidIP, diag.FragmentDrops, diag.TCPGaps, diag.ParseErrors, diag.ResourceDrops, diag.Midstream)
	if err != nil {
		logf("stopped with error: %v", err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
