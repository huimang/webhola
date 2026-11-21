package desktop

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	discoverypkg "webhola/native/internal/discovery"
	i18npkg "webhola/native/internal/i18n"
	jvmpkg "webhola/native/internal/jvm"
)

func TestArthasSettingsPreserved(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	want := settings{ArthasConfig: jvmpkg.ArthasConfig{BootPath: `D:\tools\arthas-boot.jar`, HTTPPort: "18563"}}
	if err := writeSettings(p, want); err != nil {
		t.Fatal(err)
	}
	// Saving TShark alone must not erase the optional JVM dependencies.
	// Use an empty draft to avoid requiring external programs here.
	want.BootPath = ""
	if err := writeSettings(p, want); err != nil {
		t.Fatal(err)
	}
	if err := persistSettings(context.Background(), p, ""); err != nil {
		t.Fatal(err)
	}
	got, err := readSettings(p)
	if err != nil || got != want {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := (jvmpkg.ArthasConfig{HTTPPort: "0"}).Port(); err == nil {
		t.Fatal("accepted port 0")
	}
	if _, err := (jvmpkg.ArthasConfig{BootPath: p}).Boot(); err == nil {
		t.Fatal("accepted JSON as JAR")
	}
}

func TestResolveJVMTarget(t *testing.T) {
	list := []discoverypkg.Listener{{Port: 8080, PID: 101, Process: "java.exe", JVM: true}, {Port: 8080, PID: 102, Process: "java.exe", JVM: true}}
	if _, err := resolveJVMTarget(list, 8080, 0); err == nil {
		t.Fatal("ambiguous port accepted")
	}
	if got, err := resolveJVMTarget(list, 8080, 101); err != nil || got.PID != 101 {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := resolveJVMTarget(list, 8080, 103); err == nil {
		t.Fatal("stale PID accepted")
	}
	list[0].JVM = false
	if _, err := resolveJVMTarget(list, 8080, 101); err == nil {
		t.Fatal("non JVM accepted")
	}
}

func TestArthasCommandsAndRoutes(t *testing.T) {
	m := jvmpkg.JavaMethod{ClassName: "demo.Controller$Inner", MethodName: "hello", Loader: "1ab"}
	cmd, err := jvmpkg.ObservationCommand("watch", m)
	if err != nil || !strings.Contains(cmd, `Controller\$Inner`) || !strings.Contains(cmd, "--classloader 1ab") || !strings.Contains(cmd, "-n 100000") {
		t.Fatal(cmd, err)
	}
	for _, bad := range []string{"demo.*", "demo.X;stop", "demo.X'", "demo.X\nstop"} {
		m.ClassName = bad
		if _, err := jvmpkg.ObservationCommand("trace", m); err == nil {
			t.Fatal("command injection accepted", bad)
		}
	}
	var routes []jvmpkg.JavaRoute
	var data any
	_ = json.Unmarshal([]byte(`[[{"mapping":"{GET [/users/{id}]}","className":"demo.Users","methodName":"get","loader":"abc","signature":"public java.lang.String get(int)"}]]`), &data)
	jvmpkg.CollectRoutes(data, &routes)
	if len(routes) != 1 || !routeMatches(routes[0], "GET") || !routeMatches(routes[0], "/users/") {
		t.Fatal(routes)
	}
	nodes := jvmpkg.TraceMethods(json.RawMessage(`{"type":"thread","children":[{"className":"demo.Users","methodName":"get","totalCost":2000000,"children":[{"className":"demo.Service","methodName":"find","totalCost":1000000}]}]}`))
	if len(nodes) != 2 || nodes[1].Depth != 2 || nodes[0].Cost != 2 {
		t.Fatal(nodes)
	}
	fields := []string{"{GET [/中文/{id}]}", "demo.Users", "get", "String get(int)", "abc"}
	for i, s := range fields {
		fields[i] = base64.StdEncoding.EncodeToString([]byte(s))
	}
	encoded, _ := json.Marshal("@String[WEBHOLA1;1;" + strings.Join(fields, "|") + ";]")
	decoded, count, err := jvmpkg.DecodeRouteValue(encoded)
	if err != nil || count != 1 || len(decoded) != 1 || decoded[0].Mapping != "{GET [/中文/{id}]}" {
		t.Fatal(decoded, count, err)
	}
	for _, bad := range []string{"@String[WEBHOLA1;1;cut]", "WEBHOLA1;1;onefield;", "unsupported"} {
		b, _ := json.Marshal(bad)
		if _, _, err := jvmpkg.DecodeRouteValue(b); err == nil {
			t.Fatal("invalid route result accepted", bad)
		}
	}
	var reply jvmpkg.ArthasReply
	_ = json.Unmarshal([]byte(`{"body":{"results":[{"type":"sc","classInfo":{"name":"demo.Users","classLoaderHash":"abc"}}]}}`), &reply)
	if got := jvmpkg.SearchClassInfos(reply); len(got) != 1 || got[0].Hash != "abc" {
		t.Fatal(got)
	}
}

func TestArthasObservationCancellationAndPID(t *testing.T) {
	var mu sync.Mutex
	var actions []string
	ready := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		a := p["action"].(string)
		mu.Lock()
		actions = append(actions, a)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch a {
		case "init_session":
			_, _ = w.Write([]byte(`{"state":"SUCCEEDED","sessionId":"s1","consumerId":"c1"}`))
		case "async_exec":
			_, _ = w.Write([]byte(`{"state":"SCHEDULED","body":{"jobId":7}}`))
		case "pull_results":
			select {
			case ready <- struct{}{}:
			default:
			}
			_, _ = w.Write([]byte(`{"state":"SUCCEEDED","body":{"results":[{"type":"welcome","pid":"99"}]}}`))
		default:
			_, _ = w.Write([]byte(`{"state":"SUCCEEDED"}`))
		}
	}))
	defer server.Close()
	c := &jvmpkg.ArthasClient{Endpoint: server.URL, Pid: 100, Http: server.Client()}
	if err := c.Verify(context.Background()); err == nil || !strings.Contains(i18npkg.TranslateText("zh-CN", i18npkg.ErrorMessage(err)), "目标为 PID 100") {
		t.Fatal("wrong PID accepted", err)
	}
	<-ready
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Observe(ctx, "watch demo.X get -n 5", func(map[string]json.RawMessage) {}) }()
	<-ready
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("observation did not cancel")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(actions) < 2 || actions[len(actions)-2] != "interrupt_job" || actions[len(actions)-1] != "close_session" {
		t.Fatal("session not cleaned", actions)
	}
}

func TestArthasRouteView(t *testing.T) {
	a := newApp(context.Background())
	a.arthas = &arthasState{ctx: context.Background(), status: "已连接", routes: []jvmpkg.JavaRoute{{JavaMethod: jvmpkg.JavaMethod{ClassName: "demo.Controller", MethodName: "hello", Loader: "abc"}, Mapping: "{GET [/hello]}"}}}
	tt := ui.NewTester(a.arthasView, 1180, 820)
	if !tt.HasText("选择 URL 路由") || !tt.HasText("watch") || !tt.HasText("trace") {
		t.Fatal("missing route controls")
	}
	if err := tt.Click("选择 URL 路由"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click(a.arthas.routes[0].Mapping); err != nil {
		t.Fatal(err)
	}
	if a.arthas.method.MethodName != "hello" {
		t.Fatal("route selection failed")
	}
	if preview := os.Getenv("WEBHOLA_ARTHAS_PREVIEW"); preview != "" {
		f, err := os.Create(preview)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, tt.Image()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestArthasEntryRequiresKnownJVM(t *testing.T) {
	for _, tc := range []struct {
		name     string
		jvm      bool
		pid      uint32
		selected bool
		port     string
		want     bool
	}{
		{"unselected", true, 101, false, "8080", false},
		{"non JVM", false, 101, true, "8080", false},
		{"unknown PID", true, 0, true, "8080", false},
		{"edited port", true, 101, true, "9090", false},
		{"confirmed JVM", true, 101, true, "8080", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newApp(context.Background())
			s := discoverypkg.HTTPService{Listener: discoverypkg.Listener{IP: "127.0.0.1", Port: 8080, PID: tc.pid, JVM: tc.jvm}, Status: 200}
			a.services = []discoverypkg.HTTPService{s}
			a.port = tc.port
			if tc.selected {
				a.serviceChoice = s.Key()
			}
			tt := ui.NewTester(a.serviceScanView, 1200, 100)
			if tt.HasText("JVM 路由与跟踪…") != tc.want {
				t.Fatal("Arthas entry visible for an unconfirmed target")
			}
			if !tc.want {
				a.openArthas()
				if a.arthas != nil || a.arthasCleanup != nil {
					t.Fatal("unconfirmed target opened an Arthas session")
				}
			}
		})
	}
}
