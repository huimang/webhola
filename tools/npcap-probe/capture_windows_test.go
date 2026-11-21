//go:build windows && amd64

package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
	"unsafe"
)

func TestNpcapABI(t *testing.T) {
	var h packetHeader
	var d pcapDevice
	var b bpfProgram
	if unsafe.Sizeof(h) != 16 || unsafe.Offsetof(h.Caplen) != 8 || unsafe.Sizeof(d) != 40 || unsafe.Offsetof(d.Flags) != 32 || unsafe.Sizeof(b) != 16 || unsafe.Offsetof(b.Instructions) != 8 {
		t.Fatal("Npcap Windows amd64 ABI mismatch")
	}
}

func TestNpcapLive(t *testing.T) {
	if os.Getenv("NPCAP_PROBE_INTEGRATION") != "1" {
		t.Skip("set NPCAP_PROBE_INTEGRATION=1 for installed Npcap")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if err := capture(ctx, options{selftest: true}); err != nil {
		t.Fatal(err)
	}
	// A quiet capture must honor cancellation, even when no packet ever arrives.
	d, err := loadNpcap("")
	if err != nil {
		t.Fatal(err)
	}
	defer d.dll.Release()
	devices, err := d.devices()
	if err != nil {
		t.Fatal(err)
	}
	for _, dev := range devices {
		if dev.Loopback {
			s, err := d.openSource(dev.ID, 1)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			idle, stop := context.WithTimeout(context.Background(), 120*time.Millisecond)
			defer stop()
			start := time.Now()
			_, err = s.ReadPacket(idle)
			if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
				t.Fatalf("idle cancellation: %v %s", err, time.Since(start))
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("no loopback capture device")
}
func TestLocalHTTPFixture(t *testing.T) {
	_, send, cleanup, err := prepareSelfTest(0)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := send(); err != nil {
		t.Fatal(err)
	}
}
