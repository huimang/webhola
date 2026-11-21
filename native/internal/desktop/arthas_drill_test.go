package desktop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	jvmpkg "webhola/native/internal/jvm"
)

func TestDrillDuringTraceStopsSessionBeforeSelectingMethod(t *testing.T) {
	var mu sync.Mutex
	var actions []string
	pulls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		action, _ := p["action"].(string)
		mu.Lock()
		actions = append(actions, action)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch action {
		case "init_session":
			_, _ = w.Write([]byte(`{"state":"SUCCEEDED","sessionId":"trace-session","consumerId":"consumer"}`))
		case "async_exec":
			_, _ = w.Write([]byte(`{"state":"SCHEDULED","body":{"jobId":7}}`))
		case "pull_results":
			pulls++
			if pulls == 1 {
				_, _ = w.Write([]byte(`{"state":"SUCCEEDED","body":{"results":[{"type":"trace","jobId":7,"root":{"className":"demo.Service","methodName":"load","totalCost":1000000}}]}}`))
			} else {
				<-r.Context().Done()
			}
		case "exec":
			if p["command"] != "sc -d demo.Service" {
				t.Error("unexpected drill command", p["command"])
			}
			_, _ = w.Write([]byte(`{"state":"SUCCEEDED","body":{"results":[{"type":"sc","classInfo":{"name":"demo.Service","classLoaderHash":"abc"}}]}}`))
		default:
			_, _ = w.Write([]byte(`{"state":"SUCCEEDED"}`))
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newApp(ctx)
	updates := make(chan func(), 16)
	a.dispatch = func(fn func()) { updates <- fn }
	s := &arthasState{ctx: ctx, method: jvmpkg.JavaMethod{ClassName: "demo.Controller", MethodName: "hello", Loader: "abc"}}
	s.setConnection(&jvmpkg.ArthasClient{Endpoint: server.URL, Http: server.Client()})
	a.arthas = s
	a.observeArthas(s, "trace")
	deadline := time.After(5 * time.Second)
	for len(s.records) == 0 {
		select {
		case fn := <-updates:
			fn()
		case <-deadline:
			t.Fatal("no trace result")
		}
	}
	if !s.busy || !s.observing {
		t.Fatal("trace should still be waiting for more requests")
	}
	tt := ui.NewTester(a.arthasView, 1180, 820)
	if err := tt.Click("下探"); err != nil {
		t.Fatal(err)
	}
	if s.pendingDrill == nil || s.method.ClassName != "demo.Controller" {
		t.Fatal("did not queue drill until observation cleanup")
	}
	for s.busy || s.pendingDrill != nil {
		select {
		case fn := <-updates:
			fn()
		case <-deadline:
			t.Fatal("drill did not complete after stopping trace")
		}
	}
	a.workers.Wait()
	if s.method.ClassName != "demo.Service" || s.method.MethodName != "load" || s.method.Loader != "abc" || len(s.records) != 1 {
		t.Fatal("wrong selected method or lost history", s.method, len(s.records))
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(strings.Join(actions, ","), "interrupt_job,close_session,exec") {
		t.Fatal("drill started before observation cleanup", actions)
	}
}
