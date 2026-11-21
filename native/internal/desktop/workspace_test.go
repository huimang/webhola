package desktop

import (
	"context"
	"image/png"
	"os"
	"testing"

	"github.com/egoist/mygo/ui"

	jvmpkg "webhola/native/internal/jvm"
)

func TestWorkspaceTabsRetainSessionAndClose(t *testing.T) {
	a := newApp(context.Background())
	a.loadDemo()
	ctx, cancel := context.WithCancel(a.ctx)
	defer cancel()
	s := &arthasState{ctx: ctx, cancel: cancel, status: "tab-session-status", method: jvmpkg.JavaMethod{ClassName: "demo.Controller", MethodName: "hello"}}
	a.arthas = s
	tt := ui.NewTester(a.view, 1200, 820)
	if err := tt.Click("JVM 跟踪"); err != nil {
		t.Fatal(err)
	}
	if a.workspace != 1 || !tt.HasText("tab-session-status") {
		t.Fatal("JVM tab content missing")
	}
	if path := os.Getenv("WEBHOLA_WORKSPACE_PREVIEW"); path != "" {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(f, tt.Image())
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := tt.Click("HTTP 抓包"); err != nil {
		t.Fatal(err)
	}
	if a.workspace != 0 || !tt.HasText("POST http://localhost:8080/api/preview") || ctx.Err() != nil || a.arthas != s {
		t.Fatal("switch lost HTTP content or JVM session")
	}
	a.openArthas()
	tt = ui.NewTester(a.view, 1200, 820)
	if a.workspace != 1 || a.arthas != s {
		t.Fatal("reopen did not reuse JVM tab")
	}
	if err := tt.Click("关闭 JVM 标签页"); err != nil {
		t.Fatal(err)
	}
	if a.arthas != nil || a.workspace != 0 || ctx.Err() == nil || tt.HasText("JVM 跟踪") {
		t.Fatal("close did not cancel and remove JVM tab")
	}
	if len(a.entries) == 0 {
		t.Fatal("close lost capture history")
	}
}
