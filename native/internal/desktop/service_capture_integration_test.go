package desktop

import (
	"context"
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

// Capture a real request after choosing the second port row.
// The listeners and traffic belong only to this test process.
func TestSelectedPortLiveCapture(t *testing.T) {
	if os.Getenv("WEBHOLA_INTEGRATION") != "1" {
		t.Skip("requires real TShark / Npcap")
	}
	tool, err := capturepkg.FindTShark()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	adapters, err := capturepkg.Devices(ctx, tool)
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
	args, err := capturepkg.CaptureArgs(captureID, a.port, "")
	if err != nil {
		t.Fatal(err)
	}
	captureCtx, stop := context.WithCancel(ctx)
	events := make(chan capturepkg.Exchange, 32)
	done := make(chan error, 1)
	go func() {
		done <- capturepkg.Capture(captureCtx, tool, args, func() {}, func(e capturepkg.Exchange) {
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
