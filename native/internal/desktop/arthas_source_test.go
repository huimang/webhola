package desktop

import (
	"context"
	"encoding/json"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	i18npkg "webhola/native/internal/i18n"
	jvmpkg "webhola/native/internal/jvm"
)

func TestClassSourceUsesExactClassAndLoader(t *testing.T) {
	var commands []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		command, _ := payload["command"].(string)
		commands = append(commands, command)
		if strings.HasPrefix(command, "sc ") {
			_, _ = w.Write([]byte(`{"state":"SUCCEEDED","body":{"results":[{"type":"sc","classInfo":{"name":"demo.Service$Inner","classLoaderHash":"abc"}},{"type":"sc","classInfo":{"name":"demo.Other","classLoaderHash":"def"}}]}}`))
		} else {
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "SUCCEEDED", "body": map[string]any{"results": []any{map[string]any{"type": "jad", "source": "public class Inner {}"}}}})
		}
	}))
	defer server.Close()
	c := &jvmpkg.ArthasClient{Endpoint: server.URL, Http: server.Client()}
	loaders, err := c.SourceLoaders(context.Background(), "demo.Service$Inner")
	if err != nil || len(loaders) != 1 || loaders[0] != "abc" {
		t.Fatalf("unexpected loaders: %v %v", loaders, err)
	}
	source, err := c.ClassSource(context.Background(), "demo.Service$Inner", "abc")
	if err != nil || source != "public class Inner {}" || commands[1] != `jad -E -c abc --source-only '^demo\.Service\$Inner$'` {
		t.Fatalf("wrong source target: %v %q %v", commands, source, err)
	}
	for _, method := range []jvmpkg.JavaMethod{{ClassName: "demo.Service;stop", Loader: "abc"}, {ClassName: "demo.Service", Loader: "abc;stop"}, {ClassName: "demo.Service", Loader: ""}} {
		if _, err := c.ClassSource(context.Background(), method.ClassName, method.Loader); err == nil {
			t.Fatal("invalid source target accepted")
		}
	}
	if len(commands) != 2 {
		t.Fatal("invalid target executed a command")
	}
}

func TestWatchAndTraceClassesOpenReadOnlyHighlightedSource(t *testing.T) {
	const source = "package demo;\npublic class Service {\n    // 中文注释\n    public String load() { return \"hello\"; }\n}\n"
	for _, kind := range []string{"watch", "trace"} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]any
				_ = json.NewDecoder(r.Body).Decode(&payload)
				command, _ := payload["command"].(string)
				if strings.HasPrefix(command, "sc ") {
					_, _ = w.Write([]byte(`{"state":"SUCCEEDED","body":{"results":[{"type":"sc","classInfo":{"name":"demo.Service","classLoaderHash":"abc"}}]}}`))
				} else if strings.HasPrefix(command, "jad ") {
					_ = json.NewEncoder(w).Encode(map[string]any{"state": "SUCCEEDED", "body": map[string]any{"results": []any{map[string]any{"type": "jad", "source": source}}}})
				} else {
					t.Errorf("source viewer changed observation: %s", command)
				}
			}))
			defer server.Close()
			a := newApp(context.Background())
			updates := make(chan func(), 8)
			a.dispatch = func(fn func()) { updates <- fn }
			method := jvmpkg.JavaMethod{ClassName: "demo.Service", MethodName: "load", Loader: "abc"}
			s := &arthasState{ctx: a.ctx, busy: true, observing: true}
			s.setConnection(&jvmpkg.ArthasClient{Endpoint: server.URL, Http: server.Client()})
			s.acceptRecord(jvmpkg.ArthasRecord{Kind: kind, Method: method, Structured: true, Result: `"hello"`, Chain: []jvmpkg.TraceMethod{{JavaMethod: jvmpkg.JavaMethod{ClassName: "demo.Service", MethodName: "load"}}}})
			a.arthas = s
			a.workspace = 1
			tt := ui.NewTester(a.view, 1200, 820)
			label := "demo.Service#load"
			// For trace, select the call-tree class rather than the detail title.
			if kind == "trace" {
				s.records[0].Method.ClassName = "demo.Controller"
				tt = ui.NewTester(a.view, 1200, 820)
			}
			if err := tt.Click(a.tr(i18npkg.MessageText("source.view_class", label))); err != nil {
				t.Fatal(err)
			}
			if a.javaSource == nil {
				t.Fatal("class click did not open source")
			}
			v := a.javaSource
			if a.selectedJavaSource() != v || len(a.javaSources) != 1 || !tt.HasText("Service.java") {
				t.Fatal("source icon did not activate a workspace tab")
			}
			defer a.closeJavaSource(v)
			select {
			case update := <-updates:
				update()
			case <-time.After(3 * time.Second):
				t.Fatal("source did not load")
			}
			a.workers.Wait()
			if v.editor == nil || v.editor.Buffer.Language() == nil || !v.editor.ReadOnly || v.editor.Buffer.String() != source {
				t.Fatalf("source is not read-only Java: %+v", v)
			}
			if !s.busy || !s.observing || len(s.records) != 1 {
				t.Fatal("viewing source interrupted observation")
			}
			viewer := ui.NewTester(func(c *ui.Context) { a.javaSourceView(c, v) }, 1100, 760)
			if err := viewer.Click(a.tr("source.code")); err != nil {
				t.Fatal(err)
			}
			viewer.Key(ui.Cmd, ui.KeyA)
			viewer.Key(ui.Cmd, ui.KeyC)
			if viewer.Clipboard() != source {
				t.Fatal("source cannot be copied")
			}
			viewer.Type("changed")
			if v.editor.Buffer.String() != source {
				t.Fatal("source was editable")
			}
			viewer.SetDark(true)
			if v.editor.Theme.Background != brandTheme(true).Surface {
				t.Fatal("source viewer did not follow dark mode")
			}
			if path := os.Getenv("WEBHOLA_SOURCE_PREVIEW"); path != "" && kind == "watch" {
				f, err := os.Create(path)
				if err != nil {
					t.Fatal(err)
				}
				err = png.Encode(f, viewer.Image())
				f.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestSourceRequiresLoaderChoiceWhenClassIsAmbiguous(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		command, _ := payload["command"].(string)
		if strings.HasPrefix(command, "sc ") {
			_, _ = w.Write([]byte(`{"state":"SUCCEEDED","body":{"results":[{"type":"sc","classInfo":{"name":"demo.Service","classLoaderHash":"abc"}},{"type":"sc","classInfo":{"name":"demo.Service","classLoaderHash":"def"}}]}}`))
		} else {
			if command != `jad -E -c def --source-only '^demo\.Service$'` {
				t.Errorf("wrong loader chosen: %s", command)
			}
			_, _ = w.Write([]byte(`{"state":"SUCCEEDED","body":{"results":[{"type":"jad","source":"class Service {}"}]}}`))
		}
	}))
	defer server.Close()
	a := newApp(context.Background())
	updates := make(chan func(), 8)
	a.dispatch = func(fn func()) { updates <- fn }
	s := &arthasState{ctx: a.ctx}
	s.setConnection(&jvmpkg.ArthasClient{Endpoint: server.URL, Http: server.Client()})
	a.arthas = s
	a.openJavaSource(s, jvmpkg.JavaMethod{ClassName: "demo.Service"})
	v := a.javaSource
	defer a.closeJavaSource(v)
	select {
	case update := <-updates:
		update()
	case <-time.After(3 * time.Second):
		t.Fatal("loader discovery timed out")
	}
	if v.editor != nil || v.method.Loader != "" || len(v.loaders) != 2 || v.status != i18npkg.MessageText("source.choose_loader") {
		t.Fatal("ambiguous loader was guessed")
	}
	viewer := ui.NewTester(func(c *ui.Context) { a.javaSourceView(c, v) }, 1100, 760)
	if err := viewer.Click(a.tr("source.loader_choice")); err != nil {
		t.Fatal(err)
	}
	if err := viewer.Click("def"); err != nil {
		t.Fatal(err)
	}
	select {
	case update := <-updates:
		update()
	case <-time.After(3 * time.Second):
		t.Fatal("source load timed out")
	}
	a.workers.Wait()
	if v.editor == nil || v.editor.Buffer.String() != "class Service {}" {
		t.Fatal("selected loader source did not load")
	}
}

func TestSourceUnavailableDoesNotProduceFakeCode(t *testing.T) {
	for _, reply := range []string{
		`{"state":"SUCCEEDED","body":{"results":[]}}`,
		`{"state":"SUCCEEDED","body":{"results":[{"type":"jad","source":"class A {}"},{"type":"jad","source":"class B {}"}]}}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(reply)) }))
		c := &jvmpkg.ArthasClient{Endpoint: server.URL, Http: server.Client()}
		if source, err := c.ClassSource(context.Background(), "demo.Service", "abc"); err == nil || source != "" {
			t.Fatal("missing or ambiguous source was accepted")
		}
		server.Close()
	}
}
