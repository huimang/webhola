package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"
)

type options struct {
	port, count              int
	duration                 time.Duration
	dll, device              string
	loopback, selftest, list bool
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}

func run() error {
	var o options
	flag.BoolVar(&o.list, "list", false, "List Npcap capture devices")
	flag.IntVar(&o.port, "port", 0, "TCP port; required except with -list or -self-test")
	flag.IntVar(&o.count, "count", 0, "Stop after N TCP packets; 0 means no limit")
	flag.DurationVar(&o.duration, "duration", 15*time.Second, "Capture timeout")
	flag.StringVar(&o.dll, "dll", "", "Absolute path to installed Npcap wpcap.dll; default: system Npcap directory")
	flag.StringVar(&o.device, "device", "", "Exact capture device ID from -list")
	flag.BoolVar(&o.loopback, "loopback", false, "Select Npcap loopback capture device")
	flag.BoolVar(&o.selftest, "self-test", false, "Verify local HTTP request and response capture")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flag.Args())
	}
	if o.port < 0 || o.port > 65535 || o.count < 0 || o.duration <= 0 {
		return fmt.Errorf("invalid port, count, or duration")
	}
	if o.selftest && (o.device != "" || o.count != 0) {
		return fmt.Errorf("-self-test cannot be combined with -device or -count")
	}
	if o.device != "" && o.loopback {
		return fmt.Errorf("choose either -device or -loopback")
	}
	if !o.list && !o.selftest && o.port == 0 {
		return fmt.Errorf("specify -port N, -self-test, or -list")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, o.duration)
	defer cancel()
	return capture(ctx, o)
}
