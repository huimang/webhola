package desktop

import (
	"context"
	"image/png"
	"os"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	capturepkg "webhola/native/internal/capture"
	discoverypkg "webhola/native/internal/discovery"
)

func TestRequestSelectionAndResponseTab(t *testing.T) {
	a := newApp(context.Background())
	a.loadDemo()
	tt := ui.NewTester(a.view, 1200, 820)
	if err := tt.Click("POST http://localhost:8080/api/preview"); err != nil {
		t.Fatal(err)
	}
	if a.selected != 2 {
		t.Fatal("selected request did not change")
	}
	if err := tt.Click("响应"); err != nil {
		t.Fatal(err)
	}
	if a.tab != "http.response" || !tt.HasText("201 · 31.0 ms") {
		t.Fatal("response not visible")
	}
	if err := tt.Click("HTTP 起始行与头部"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Key(ui.Cmd, ui.KeyC)
	if !strings.Contains(tt.Clipboard(), "HTTP/1.1 201 Created") {
		t.Fatal("selected response headers cannot be copied")
	}
	if err := tt.Click("清空记录"); err != nil {
		t.Fatal(err)
	}
	if len(a.entries) != 0 || !tt.HasText("还没有捕获到请求") {
		t.Fatal("clear failed")
	}
}

func TestWaitingResponse(t *testing.T) {
	a := newApp(context.Background())
	a.accept(capturepkg.Exchange{ID: 1, Method: "GET", URL: "/pending"})
	a.tab = "http.response"
	tt := ui.NewTester(a.view, 1200, 820)
	if !tt.HasText("尚未捕获到最终响应") {
		t.Fatal("pending response is unclear")
	}
}

func TestOptionalPreview(t *testing.T) {
	path := os.Getenv("WEBHOLA_PREVIEW")
	if path == "" {
		t.Skip("optional preview artifact")
	}
	a := newApp(context.Background())
	a.loadDemo()
	a.tab = "http.response"
	a.deviceList = []capturepkg.Device{{ID: "1", Label: `\Device\NPF_Loopback (Loopback)`}}
	a.services = []discoverypkg.HTTPService{{Listener: discoverypkg.Listener{IP: "127.0.0.1", Port: 8080, PID: 123, Process: "node.exe"}, Status: 200}}
	a.chooseService(a.services[0].Key())
	a.scanStatus = "示例扫描结果 · 已自动填写端口并选择 loopback。"
	tt := ui.NewTester(a.view, 1200, 820)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = png.Encode(f, tt.Image()); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("格式化"); err != nil {
		t.Fatal(err)
	}
	a.openBodyFullscreen(a.bodyPreview)
	full := ui.NewTester(a.bodyFullscreenView, 1400, 900)
	ff, err := os.Create(path + "-fullscreen.png")
	if err != nil {
		t.Fatal(err)
	}
	if err = png.Encode(ff, full.Image()); err != nil {
		t.Fatal(err)
	}
	ff.Close()
	about := ui.NewTester(a.aboutView, 560, 420)
	af, err := os.Create(path + "-about.png")
	if err != nil {
		t.Fatal(err)
	}
	defer af.Close()
	if err = png.Encode(af, about.Image()); err != nil {
		t.Fatal(err)
	}
}
