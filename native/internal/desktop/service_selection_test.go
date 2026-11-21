package desktop

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	capturepkg "webhola/native/internal/capture"
	discoverypkg "webhola/native/internal/discovery"
)

func portSelectionApp() *app {
	a := newApp(context.Background())
	a.deviceList = []capturepkg.Device{{ID: "9", Label: `\Device\NPF_Loopback (Loopback)`}, {ID: "6", Label: `\Device\NPF_{WIFI} (WLAN)`}}
	a.serviceAdapters = []discoverypkg.LocalAdapter{{Name: "WLAN", CaptureName: `\Device\NPF_{WIFI}`, Addresses: []net.IP{net.ParseIP("192.168.1.10")}}}
	list := []discoverypkg.Listener{
		{IP: "127.0.0.1", Port: 8081, PID: 11, Process: "java.exe", JVM: true},
		{IP: "::", Port: 8081, PID: 11, Process: "java.exe", JVM: true},
		{IP: "0.0.0.0", Port: 8082, PID: 11, Process: "java.exe", JVM: true},
	}
	a.services = discoverypkg.ScanListeners(a.ctx, list, func(_ context.Context, l discoverypkg.Listener) (discoverypkg.HTTPService, bool) {
		return discoverypkg.HTTPService{Listener: l, Status: 200}, true
	}, nil)
	return a
}

func TestPortRowsPreserveBindingsAndLANAdapter(t *testing.T) {
	a := portSelectionApp()
	if len(a.services) != 2 || len(a.services[0].EndpointList()) != 2 || a.services[0].Port != 8081 || a.services[1].Port != 8082 {
		t.Fatal("expected one row per port with all bindings retained", a.services)
	}
	a.iface = a.deviceList[1].Label
	for _, s := range a.services {
		a.chooseService(s.Key())
		if a.iface != a.deviceList[1].Label {
			t.Fatal("port selection overwrote WLAN", a.iface)
		}
		if jvm, ok := a.selectedJVMService(); !ok || jvm.Port != s.Port || jvm.PID != 11 {
			t.Fatal("wrong JVM target", jvm, ok)
		}
	}
	a.ifaceManual = true
	a.iface = discoverypkg.DefaultCaptureDevice(a.deviceList, a.iface)
	a.chooseService(a.services[0].Key())
	if a.iface != a.deviceList[1].Label || a.port != "8081" {
		t.Fatal("refresh lost selected adapter or port")
	}
	args, err := capturepkg.CaptureArgs("6", a.port, "")
	if err != nil || !strings.Contains(strings.Join(args, " "), "tcp port 8081") {
		t.Fatal(args, err)
	}
	a.running = true
	a.chooseService(a.services[1].Key())
	if a.port != "8081" {
		t.Fatal("capture target changed while running")
	}
}

func TestPortRowCannotAttachAmbiguousJVM(t *testing.T) {
	a := portSelectionApp()
	a.services[0].Endpoints = append(a.services[0].Endpoints, discoverypkg.HTTPService{Listener: discoverypkg.Listener{IP: "192.168.1.10", Port: 8081, PID: 22, JVM: true}})
	a.chooseService(a.services[0].Key())
	if _, ok := a.selectedJVMService(); ok {
		t.Fatal("attached to an ambiguous JVM")
	}
}

func TestPortDropdownHasNoExtraEndpointSelectors(t *testing.T) {
	a := portSelectionApp()
	a.iface = a.deviceList[1].Label
	a.ifaceManual = true
	tt := ui.NewTester(a.view, 1200, 820)
	if err := tt.Click("已发现的 HTTP 服务"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click(a.serviceLabel(a.services[1])); err != nil {
		t.Fatal(err)
	}
	if a.port != "8082" || a.iface != a.deviceList[1].Label {
		t.Fatal("dropdown failed to select port and preserve WLAN")
	}
	for _, label := range []string{"进程端口", "监听地址", "自动匹配网卡"} {
		if _, ok := tt.Find(label); ok {
			t.Fatal("obsolete endpoint control visible", label)
		}
	}
	for _, label := range []string{"已发现的 HTTP 服务", "监听网卡", "HTTP 端口"} {
		if _, ok := tt.Find(label); !ok {
			t.Fatal("required control missing", label)
		}
	}
	a.language = "en"
	tt = ui.NewTester(a.view, 1200, 820)
	if !tt.HasText("Discovered services") {
		t.Fatal("service label not translated")
	}
}

func TestWildcardServicePreservesLANAdapter(t *testing.T) {
	for _, address := range []string{"0.0.0.0", "::"} {
		a := portSelectionApp()
		a.iface = a.deviceList[1].Label
		s := discoverypkg.HTTPService{Listener: discoverypkg.Listener{IP: address, Port: 8081, PID: 99}, Status: 200}
		a.services = []discoverypkg.HTTPService{s}
		a.chooseService(s.Key())
		if a.iface != a.deviceList[1].Label {
			t.Fatal("wildcard replaced WLAN")
		}
		a.iface = ""
		a.chooseService(s.Key())
		if a.iface != "" {
			t.Fatal("wildcard guessed adapter")
		}
	}
}
