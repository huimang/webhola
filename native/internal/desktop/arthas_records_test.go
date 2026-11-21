package desktop

import (
	"context"
	"encoding/json"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	i18npkg "webhola/native/internal/i18n"
	jvmpkg "webhola/native/internal/jvm"
)

func watchTestRecord(t *testing.T, route jvmpkg.JavaRoute, text string) jvmpkg.ArthasRecord {
	t.Helper()
	value, _ := json.Marshal("@String[" + text + "]")
	record, ok := jvmpkg.NewArthasRecord(map[string]json.RawMessage{"type": json.RawMessage(`"watch"`), "value": value, "cost": json.RawMessage(`2.75`), "ts": json.RawMessage(`1700000000000`)}, route.JavaMethod, jvmpkg.RouteKey(route), jvmpkg.RouteLabel(route))
	if !ok || !record.Structured {
		t.Fatal("structured watch result not decoded", record)
	}
	return record
}

func TestArthasWatchFieldsAndHistory(t *testing.T) {
	route := jvmpkg.JavaRoute{JavaMethod: jvmpkg.JavaMethod{ClassName: "demo.Controller", MethodName: "hello", Loader: "abc"}, Mapping: "{GET [/hello/{name}]}"}
	r := watchTestRecord(t, route, `[["世界 ],:",{"id":9007199254740993}],{"ok":true,"items":[1,2]},null]`)
	if r.Failed || r.Cost != 2.75 || !strings.Contains(r.Params, "9007199254740993") || r.Result != `{"ok":true,"items":[1,2]}` || r.At.UnixMilli() != 1700000000000 {
		t.Fatal(r)
	}
	failed := watchTestRecord(t, route, `[["world"],null,{"message":"failed"}]`)
	if !failed.Failed || failed.Result != "null" {
		t.Fatal(failed)
	}
	encoded := watchTestRecord(t, route, `[[],"{\"ok\":true}",null]`)
	if encoded.Result != `{"ok":true}` {
		t.Fatal(encoded.Result)
	}
	text := watchTestRecord(t, route, `[[],"null",null]`)
	if text.Result != `"null"` {
		t.Fatal("string type was lost", text.Result)
	}
	if _, ok := jvmpkg.WatchFields(json.RawMessage(`"@Object[][ambiguous, display]"`)); ok {
		t.Fatal("guessed structure from display text")
	}
	if _, ok := jvmpkg.NewArthasRecord(map[string]json.RawMessage{"type": json.RawMessage(`"enhancer"`)}, route.JavaMethod, "", ""); ok {
		t.Fatal("control message added as invocation")
	}
	s := &arthasState{routes: []jvmpkg.JavaRoute{route}}
	s.selectRoute(jvmpkg.RouteKey(route))
	s.acceptRecord(r)
	selected := s.selectedRecord
	for i := 0; i < 9; i++ {
		s.acceptRecord(r)
	}
	if s.selectedRecord != selected || len(s.records) != 10 {
		t.Fatal("new results changed the selected invocation")
	}
	for i := 0; i < jvmpkg.MaxArthasRecords; i++ {
		s.acceptRecord(r)
	}
	if len(s.records) != jvmpkg.MaxArthasRecords || s.selectedCall() == nil || s.historyBytes > jvmpkg.MaxArthasHistoryBytes {
		t.Fatal("history not bounded")
	}
	next := s.nextRecord
	s.clearRecords()
	s.acceptRecord(r)
	if s.records[0].ID <= next {
		t.Fatal("clear reused invocation IDs")
	}
	other := route
	other.Mapping = "{POST [/other]}"
	s.routes = append(s.routes, other)
	s.selectRoute(jvmpkg.RouteKey(other))
	if s.selectedCall() != nil {
		t.Fatal("another route's invocation remained selected")
	}
}

func TestWatchRecordsExcludeCGLIBProxyInvocations(t *testing.T) {
	method := jvmpkg.JavaMethod{ClassName: "dev.gymlin.server.api.member.controller.AccountController", MethodName: "profileBasic", Loader: "abc"}
	for _, suffix := range []string{"$$SpringCGLIB$$0", "$$EnhancerBySpringCGLIB$$123", "$$FastClassBySpringCGLIB$$123", "$$EnhancerByCGLIB$$123", "$$FastClassByCGLIB$$123"} {
		class, _ := json.Marshal(method.ClassName + suffix)
		result := map[string]json.RawMessage{"type": json.RawMessage(`"watch"`), "className": class, "methodName": json.RawMessage(`"profileBasic"`)}
		if _, ok := jvmpkg.NewArthasRecord(result, method, "route", "label"); ok {
			t.Fatalf("proxy invocation was kept: %s", class)
		}
		// Also exclude proxies when Arthas omits className and the selected
		// target itself supplies the proxy name.
		proxy := method
		proxy.ClassName += suffix
		delete(result, "className")
		if _, ok := jvmpkg.NewArthasRecord(result, proxy, "route", "label"); ok {
			t.Fatal("proxy target fallback was kept")
		}
		// A real business-class invocation remains visible even if the
		// observation target was a proxy.
		result["className"], _ = json.Marshal(method.ClassName)
		if r, ok := jvmpkg.NewArthasRecord(result, proxy, "route", "label"); !ok || r.Method.ClassName != method.ClassName {
			t.Fatal("business invocation was removed")
		}
		result["type"] = json.RawMessage(`"trace"`)
		result["className"] = class
		if _, ok := jvmpkg.NewArthasRecord(result, method, "route", "label"); !ok {
			t.Fatal("watch filter changed trace records")
		}
	}
}

func TestArthasMonitorSelectionHighlightAndFullscreen(t *testing.T) {
	a := newApp(context.Background())
	route := jvmpkg.JavaRoute{JavaMethod: jvmpkg.JavaMethod{ClassName: "demo.Controller", MethodName: "hello", Loader: "abc"}, Mapping: "{GET [/hello/{name}]}"}
	s := &arthasState{ctx: context.Background(), routes: []jvmpkg.JavaRoute{route}, status: i18npkg.MessageText("arthas_ui.observation_point_ready_send_up_to_5")}
	s.selectRoute(jvmpkg.RouteKey(route))
	a.arthas = s
	s.acceptRecord(watchTestRecord(t, route, `[["first"],{"id":1,"name":"中文"},null]`))
	s.acceptRecord(watchTestRecord(t, route, `[["second"],{"id":2,"ok":true},null]`))
	tt := ui.NewTester(a.arthasView, 1180, 820)
	if s.preview == nil || !s.preview.formatted || s.preview.editor == nil || s.preview.editor.Buffer.Language() == nil {
		t.Fatal("return value was not highlighted")
	}
	if err := tt.Click("WATCH #2"); err != nil {
		t.Fatal(err)
	}
	if s.selectedRecord != 2 || !strings.Contains(s.preview.source, `"id":2`) {
		t.Fatal("did not select the second invocation")
	}
	if err := tt.Click("请求参数"); err != nil {
		t.Fatal(err)
	}
	if s.preview.source != `["second"]` {
		t.Fatal(s.preview.source)
	}
	if err := tt.Click("响应结果"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("JVM 调用数据"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Key(ui.Cmd, ui.KeyC)
	if tt.Clipboard() != s.preview.text {
		t.Fatal("highlighted return value cannot be copied")
	}
	tt.Type("overwrite")
	if s.preview.editor.Buffer.String() != s.preview.text {
		t.Fatal("return value is editable")
	}
	if err := tt.Click("全屏查看"); err != nil {
		t.Fatal(err)
	}
	if a.bodyFullscreen == nil || a.bodyFullscreen == s.preview || a.bodyFullscreen.editor == s.preview.editor || a.bodyFullscreen.source != s.preview.source {
		t.Fatal("fullscreen must have an independent result snapshot")
	}
	if a.bodyPreview != nil {
		t.Fatal("JVM viewer changed the HTTP body preview")
	}
	full := ui.NewTester(a.bodyFullscreenView, 1280, 820)
	if !full.HasText("退出全屏") {
		t.Fatal("fullscreen controls missing")
	}
	full.Key(0, ui.KeyEscape)
	if a.bodyFullscreen != nil {
		t.Fatal("fullscreen did not exit")
	}
	if preview := os.Getenv("WEBHOLA_JVM_MONITOR_PREVIEW"); preview != "" {
		f, err := os.Create(filepath.Join(preview, "jvm-watch.png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, tt.Image()); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	trace, ok := jvmpkg.NewArthasRecord(map[string]json.RawMessage{"type": json.RawMessage(`"trace"`), "root": json.RawMessage(`{"type":"thread","children":[{"className":"demo.Controller","methodName":"hello","totalCost":3000000,"times":1,"children":[{"className":"demo.Service","methodName":"load","totalCost":2000000,"times":2,"lineNumber":42}]}]}`)}, route.JavaMethod, jvmpkg.RouteKey(route), jvmpkg.RouteLabel(route))
	if !ok || len(trace.Chain) != 2 || trace.Cost != 3 {
		t.Fatal(trace)
	}
	s.acceptRecord(trace)
	tt = ui.NewTester(a.arthasView, 1180, 820)
	if err := tt.Click("TRACE #3"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("调用链") || s.detailTab != "tree" {
		t.Fatal("trace details not selected")
	}
	if preview := os.Getenv("WEBHOLA_JVM_MONITOR_PREVIEW"); preview != "" {
		f, err := os.Create(filepath.Join(preview, "jvm-trace.png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, tt.Image()); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	if err := tt.Click("原始结果"); err != nil {
		t.Fatal(err)
	}
	if s.preview == nil || !s.preview.pretty {
		t.Fatal("trace raw JSON viewer missing")
	}
	if err := tt.Click("清空记录"); err != nil {
		t.Fatal(err)
	}
	if len(s.records) != 0 || s.preview != nil || s.method.MethodName != "hello" {
		t.Fatal("clear changed the observation target")
	}
}
