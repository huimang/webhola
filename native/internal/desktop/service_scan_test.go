package desktop

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	capturepkg "webhola/native/internal/capture"
	discoverypkg "webhola/native/internal/discovery"
	i18npkg "webhola/native/internal/i18n"
)

func serverListener(t *testing.T, s *httptest.Server) discoverypkg.Listener {
	t.Helper()
	host, p, err := net.SplitHostPort(s.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(p)
	return discoverypkg.Listener{IP: host, Port: port, PID: 123, Process: "dev.exe"}
}

func TestProbeHTTPRecognizesErrorsWithoutFollowingRedirects(t *testing.T) {
	var destinationHits atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinationHits.Add(1) }))
	defer destination.Close()
	for _, code := range []int{200, 301, 401, 404, 405, 500} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			var seen atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "HEAD" || r.URL.Path != "/" {
					t.Errorf("unexpected probe: %s %s", r.Method, r.URL.Path)
				}
				seen.Store(true)
				w.Header().Set("Location", destination.URL)
				w.WriteHeader(code)
			}))
			defer server.Close()
			// A broken proxy environment must never redirect local service discovery.
			t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
			got, ok := discoverypkg.ProbeHTTP(context.Background(), serverListener(t, server))
			if !ok || got.Status != code || !seen.Load() {
				t.Fatalf("HTTP service missed: %+v %v", got, ok)
			}
		})
	}
	if destinationHits.Load() != 0 {
		t.Fatal("scan followed a redirect")
	}
}

func TestProbeRejectsOtherProtocolsAndTimesOut(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.Write([]byte("SSH-2.0-Test\r\n"))
	}()
	host, p, _ := net.SplitHostPort(l.Addr().String())
	port, _ := strconv.Atoi(p)
	if _, ok := discoverypkg.ProbeHTTP(context.Background(), discoverypkg.Listener{IP: host, Port: port}); ok {
		t.Fatal("SSH service classified as HTTP")
	}
	<-done
	stalled, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer stalled.Close()
	release := make(chan struct{})
	defer close(release)
	go func() {
		c, err := stalled.Accept()
		if err == nil {
			defer c.Close()
			<-release
		}
	}()
	host, p, _ = net.SplitHostPort(stalled.Addr().String())
	port, _ = strconv.Atoi(p)
	start := time.Now()
	if _, ok := discoverypkg.ProbeHTTP(context.Background(), discoverypkg.Listener{IP: host, Port: port}); ok {
		t.Fatal("silent service classified as HTTP")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("probe did not honor timeout")
	}
}

func TestScanDeduplicatesAndCanCancel(t *testing.T) {
	var calls atomic.Int32
	found := discoverypkg.ScanListeners(context.Background(), []discoverypkg.Listener{{IP: "127.0.0.1", Port: 8080}, {IP: "127.0.0.1", Port: 8080}, {IP: "127.0.0.1", Port: 3000}}, func(_ context.Context, l discoverypkg.Listener) (discoverypkg.HTTPService, bool) {
		calls.Add(1)
		return discoverypkg.HTTPService{Listener: l, Status: 200}, true
	}, nil)
	if calls.Load() != 2 || len(found) != 2 || found[0].Port != 3000 {
		t.Fatal("duplicates or unstable ordering")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls.Store(0)
	discoverypkg.ScanListeners(ctx, []discoverypkg.Listener{{IP: "127.0.0.1", Port: 8080}}, func(context.Context, discoverypkg.Listener) (discoverypkg.HTTPService, bool) {
		calls.Add(1)
		return discoverypkg.HTTPService{}, false
	}, nil)
	if calls.Load() != 0 {
		t.Fatal("canceled scan still probed")
	}
}

func TestScanGroupsByPortAfterProbing(t *testing.T) {
	list := []discoverypkg.Listener{
		{IP: "::", Port: 8080, PID: 11, JVM: true}, {IP: "127.0.0.1", Port: 8080, PID: 11, JVM: true},
		{IP: "127.0.0.1", Port: 9000, PID: 11, JVM: true}, {IP: "127.0.0.1", Port: 7000, PID: 11, JVM: true},
		{IP: "127.0.0.1", Port: 8080, PID: 12, Process: "java.exe", JVM: true},
		{IP: "127.0.0.1", Port: 3000}, {IP: "127.0.0.1", Port: 3001},
	}
	var calls atomic.Int32
	found := discoverypkg.ScanListeners(context.Background(), list, func(_ context.Context, l discoverypkg.Listener) (discoverypkg.HTTPService, bool) {
		calls.Add(1)
		status := 200
		if l.Port == 7000 {
			status = 0
		}
		return discoverypkg.HTTPService{Listener: l, Status: status}, true
	}, nil)
	if calls.Load() != int32(len(list)) || len(found) != 5 {
		t.Fatalf("endpoints skipped or processes merged: calls=%d results=%+v", calls.Load(), found)
	}
	for _, s := range found {
		if s.Port == 8080 && (s.PID != 11 || s.IP != "127.0.0.1" || !s.JVM) {
			t.Fatal("wrong representative", s)
		}
	}
}

func TestCaptureDefaultsForLocalServices(t *testing.T) {
	devices := []capturepkg.Device{{ID: "1", Label: `\Device\NPF_{BLUETOOTH} (Bluetooth)`}, {ID: "9", Label: `\Device\NPF_Loopback (Loopback)`}}
	if got := discoverypkg.DefaultCaptureDevice(devices, ""); got != "" {
		t.Fatal("guessed an initial capture adapter", got)
	}
	if got := discoverypkg.DefaultCaptureDevice(devices, devices[0].Label); got != devices[0].Label {
		t.Fatal("refresh overwrote manual choice", got)
	}
	for _, ip := range []string{"0.0.0.0", "::"} {
		got, note := discoverypkg.MatchServiceDevice(discoverypkg.HTTPService{Listener: discoverypkg.Listener{IP: ip, Port: 8081}}, devices, nil)
		if got != "" || !strings.Contains(i18npkg.TranslateText("en", note), "WLAN") {
			t.Fatal("wildcard service guessed an adapter or omitted LAN guidance", got, note)
		}
	}
	if got, _ := discoverypkg.MatchServiceDevice(discoverypkg.HTTPService{Listener: discoverypkg.Listener{IP: "::", Port: 8081}}, devices[:1], nil); got != "" {
		t.Fatal("guessed an adapter without loopback", got)
	}
}

func TestSelectServiceMatchesAdapterAndKeepsManualPort(t *testing.T) {
	a := newApp(context.Background())
	a.iface = "existing adapter"
	a.deviceList = []capturepkg.Device{{ID: "2", Label: `\Device\NPF_Loopback (Loopback)`}, {ID: "7", Label: `\Device\NPF_{LOCAL} (Wi-Fi)`}}
	a.serviceAdapters = []discoverypkg.LocalAdapter{{Name: "Wi-Fi", CaptureName: `\Device\NPF_{LOCAL}`, Addresses: []net.IP{net.ParseIP("192.168.1.10"), net.ParseIP("fe80::123")}}}
	for _, v := range []struct{ ip, want string }{{"127.0.0.1", a.deviceList[0].Label}, {"::1", a.deviceList[0].Label}, {"192.168.1.10", a.deviceList[1].Label}, {"fe80::123%7", a.deviceList[1].Label}, {"0.0.0.0", "existing adapter"}, {"::", "existing adapter"}, {"192.168.99.99", "existing adapter"}} {
		a.iface = "existing adapter"
		s := discoverypkg.HTTPService{Listener: discoverypkg.Listener{IP: v.ip, Port: 3000, Process: "node.exe"}, Status: 200}
		a.services = []discoverypkg.HTTPService{s}
		a.chooseService(s.Key())
		if a.port != "3000" || a.iface != v.want {
			t.Fatalf("incorrect selection for %s: port=%s iface=%s", v.ip, a.port, a.iface)
		}
	}
	tt := ui.NewTester(a.view, 1100, 720)
	if err := tt.Click("HTTP 端口"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("9090")
	if a.port != "9090" || a.serviceChoice != "" {
		t.Fatal("manual input was lost or service selection remained stale")
	}
	for _, label := range []string{"扫描 HTTP 服务", "已发现的 HTTP 服务", "HTTP 端口"} {
		r, ok := tt.Find(label)
		if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > 1100 || r.Y+r.H > 720 {
			t.Fatalf("control outside window: %s %+v", label, r)
		}
	}
}

func TestServiceDropdownSelection(t *testing.T) {
	a := newApp(context.Background())
	s := discoverypkg.HTTPService{Listener: discoverypkg.Listener{IP: "127.0.0.1", Port: 3000, Process: "node.exe"}, Status: 200}
	a.services = []discoverypkg.HTTPService{s}
	a.deviceList = []capturepkg.Device{{ID: "1", Label: `\Device\NPF_Loopback (Loopback)`}}
	tt := ui.NewTester(a.view, 1200, 820)
	if err := tt.Click("已发现的 HTTP 服务"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click(a.serviceLabel(s)); err != nil {
		t.Fatal(err)
	}
	if a.port != "3000" || a.iface != a.deviceList[0].Label || !strings.Contains(a.tr(a.scanStatus), "自动选择") {
		t.Fatalf("dropdown did not apply service: %s %s", a.port, a.scanStatus)
	}
}
