package desktop

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	capturepkg "webhola/native/internal/capture"
	discoverypkg "webhola/native/internal/discovery"
)

// A connection established before capture must be taken over at the next
// complete HTTP request instead of being ignored.
func TestMidstreamLiveCapture(t *testing.T) {
	if os.Getenv("WEBHOLA_INTEGRATION") != "1" {
		t.Skip("requires real Npcap")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	adapters, err := capturepkg.ListDevices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	loop := discoverypkg.LoopbackCaptureDevice(adapters)
	if loop == "" {
		t.Skip("no loopback capture adapter")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"midstream":true}`))
	}))
	defer server.Close()
	port := serverListener(t, server).Port
	conn, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var captureID string
	for _, d := range adapters {
		if d.Label == loop {
			captureID = d.ID
		}
	}
	cfg, err := capturepkg.Config(captureID, strconv.Itoa(port), "")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Logf = t.Logf
	captureCtx, stop := context.WithCancel(ctx)
	defer stop()
	events := make(chan capturepkg.Exchange, 32)
	done := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		done <- capturepkg.RunCapture(captureCtx, cfg, "", func() { close(ready) }, func(e capturepkg.Exchange) {
			select {
			case events <- e:
			case <-captureCtx.Done():
			}
		})
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("capture did not start")
	}
	time.Sleep(100 * time.Millisecond)
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/webhola-midstream", nil)
	if _, err := fmt.Fprintf(conn, "GET /webhola-midstream HTTP/1.1\r\nHost: %s\r\nConnection: keep-alive\r\n\r\n", req.Host); err != nil {
		t.Fatal(err)
	}
	res, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("midstream request was not captured")
		case err := <-done:
			t.Fatalf("capture stopped: %v", err)
		case e := <-events:
			if e.Response != nil && strings.Contains(e.URL, "/webhola-midstream") {
				t.Logf("Captured midstream GET and HTTP %d", e.Status)
				return
			}
		}
	}
}

// Capture a real request after choosing the second port row.
// The listeners and traffic belong only to this test process.
func TestSelectedPortLiveCapture(t *testing.T) {
	if os.Getenv("WEBHOLA_INTEGRATION") != "1" {
		t.Skip("requires real Npcap")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	adapters, err := capturepkg.ListDevices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	loop := discoverypkg.LoopbackCaptureDevice(adapters)
	if loop == "" {
		t.Skip("no loopback capture adapter")
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"selectedPort":true}`))
	})
	first := httptest.NewServer(handler)
	defer first.Close()
	second := httptest.NewServer(handler)
	defer second.Close()
	ports := map[int]string{serverListener(t, first).Port: first.URL, serverListener(t, second).Port: second.URL}
	all, err := discoverypkg.ListListeners(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var own []discoverypkg.Listener
	for _, l := range all {
		if l.PID == uint32(os.Getpid()) && ports[l.Port] != "" {
			own = append(own, l)
		}
	}
	groups := discoverypkg.ScanListeners(ctx, own, discoverypkg.ProbeHTTP, nil)
	if len(groups) != 2 || groups[0].Port == groups[1].Port {
		t.Fatalf("test ports not listed separately: %+v", groups)
	}
	a := newApp(ctx)
	a.deviceList = adapters
	a.services = groups
	a.chooseService(groups[0].Key())
	var target int
	for port := range ports {
		if port != groups[0].Port {
			target = port
		}
	}
	a.chooseService((discoverypkg.HTTPService{Listener: discoverypkg.Listener{Port: target}}).Key())
	a.iface = loop
	a.ifaceManual = true
	a.applyServiceEndpoint()
	var captureID string
	for _, d := range adapters {
		if d.Label == a.iface {
			captureID = d.ID
		}
	}
	cfg, err := capturepkg.Config(captureID, a.port, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Logf = t.Logf
	captureCtx, stop := context.WithCancel(ctx)
	events := make(chan capturepkg.Exchange, 32)
	done := make(chan error, 1)
	go func() {
		done <- capturepkg.RunCapture(captureCtx, cfg, "", func() {}, func(e capturepkg.Exchange) {
			select {
			case events <- e:
			case <-captureCtx.Done():
			}
		})
	}()
	defer func() {
		stop()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("capture did not stop")
		}
	}()
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	requestURL := ports[target] + "/webhola-grouped-port-check"
	for {
		select {
		case <-ctx.Done():
			t.Fatal("selected grouped port did not capture a real request")
		case <-ticker.C:
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
			res, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
		case e := <-events:
			a.acceptCaptured(e)
			if e.Status == 200 && e.Response != nil && strings.Contains(e.URL, "/webhola-grouped-port-check") {
				if !strings.Contains(e.Server, ":"+strconv.Itoa(target)) || len(a.entries) == 0 {
					t.Fatal("capture used the wrong endpoint", e)
				}
				t.Logf("Selected port %d; captured GET and HTTP 200", target)
				return
			}
		}
	}
}
