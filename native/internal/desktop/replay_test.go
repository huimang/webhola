package desktop

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	capturepkg "webhola/native/internal/capture"
	replaypkg "webhola/native/internal/replay"
)

func TestReplayPreservesRequestAndDoesNotFollowRedirects(t *testing.T) {
	var hits, redirects atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirects.Add(1) }))
	defer target.Close()
	body := []byte{0, 1, 255, 42}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		got, _ := io.ReadAll(r.Body)
		if r.Method != "POST" || r.URL.RequestURI() != "/api?q=hello%20world" || r.Host != "dev.local" || string(got) != string(body) || r.Header.Get("Authorization") != "Bearer local-test" || r.Header.Get("Cookie") != "test=1" || len(r.Header.Values("X-Test")) != 2 || r.Header.Get("X-Hop") != "" {
			t.Errorf("request changed: %+v body=%v", r, got)
		}
		w.Header().Set("Location", target.URL)
		w.WriteHeader(302)
		io.WriteString(w, "redirect body")
	}))
	defer server.Close()
	original := capturepkg.Exchange{ID: 1, Method: "POST", URL: server.URL + "/api?q=hello%20world", Started: time.Now(), Request: capturepkg.Message{Headers: "Host: dev.local\r\nContent-Length: 4\r\nAuthorization: Bearer local-test\r\nCookie: test=1\r\nX-Test: one\r\nX-Test: two\r\nConnection: X-Hop\r\nX-Hop: removed\r\n", RawBody: body, Bytes: 4}}
	req, err := replaypkg.ReplayRequest(original)
	if err != nil {
		t.Fatal(err)
	}
	var registered atomic.Bool
	result, err := replaypkg.RunReplay(context.Background(), req, original, func(string, string) { registered.Store(true) })
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != 302 || result.Response.Body != "redirect body" || hits.Load() != 1 || redirects.Load() != 0 || !registered.Load() {
		t.Fatalf("unexpected replay: %+v hits=%d redirects=%d", result, hits.Load(), redirects.Load())
	}
}

func TestReplayRejectsIncompletePayload(t *testing.T) {
	for _, request := range []capturepkg.Message{{Body: "short", Truncated: true}, {Headers: "Content-Length: 100\r\n", Body: "short"}, {Headers: "Content-Encoding: gzip\r\n", Body: "decoded"}, {Headers: "Content-Length: invalid\r\n"}, {Body: "short", Bytes: 100}} {
		if _, err := replaypkg.ReplayRequest(capturepkg.Exchange{Method: "POST", URL: "http://127.0.0.1:1/", Request: request}); err == nil {
			t.Fatal("unsafe incomplete payload accepted")
		}
	}
}

func TestResendButtonCreatesNewRecordAndDeduplicatesCapture(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()
	a := newApp(context.Background())
	original := capturepkg.Exchange{ID: 1, Method: "GET", URL: server.URL + "/api", Started: time.Now(), Request: capturepkg.Message{Line: "GET /api HTTP/1.1"}}
	a.accept(original)
	queue := make(chan func(), 2)
	a.dispatch = func(fn func()) { queue <- fn }
	tt := ui.NewTester(a.view, 1200, 820)
	if err := tt.Click("重新发送"); err != nil {
		t.Fatal(err)
	}
	select {
	case fn := <-queue:
		fn()
	case <-time.After(5 * time.Second):
		t.Fatal("replay did not finish")
	}
	a.workers.Wait()
	if len(a.entries) != 2 || a.replaying || a.entries[0].Response != nil || !a.entries[1].Replayed || a.entries[1].Status != 200 || a.selected != a.entries[1].ID || hits.Load() != 1 {
		t.Fatalf("replay replaced original or lost result: %+v", a.entries)
	}
	replay := a.entries[1]
	a.acceptCaptured(capturepkg.Exchange{ID: 1, Client: replay.Client, Server: replay.Server, Method: "GET", URL: original.URL})
	if len(a.entries) != 2 {
		t.Fatal("passive capture duplicated replay")
	}
	a.acceptCaptured(capturepkg.Exchange{ID: 2, Client: "127.0.0.1:12345", Server: replay.Server, Method: "GET", URL: server.URL + "/other"})
	if len(a.entries) != 3 || a.entries[2].ID <= replay.ID {
		t.Fatal("capture collided with replay IDs")
	}
	a.clear()
	a.accept(replay)
	if len(a.entries) != 0 {
		t.Fatal("late replay response resurrected cleared records")
	}
}

func TestReplayFailureIsRecorded(t *testing.T) {
	a := newApp(context.Background())
	queue := make(chan func(), 2)
	a.dispatch = func(fn func()) { queue <- fn }
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()
	a.accept(capturepkg.Exchange{ID: 1, Method: "GET", URL: url})
	a.resend(a.entries[0])
	select {
	case fn := <-queue:
		fn()
	case <-time.After(5 * time.Second):
		t.Fatal("failure timed out")
	}
	a.workers.Wait()
	if len(a.entries) != 2 || a.entries[1].Error == "" || !a.failed || a.replaying {
		t.Fatal("failure not preserved")
	}
}

func TestReplayResponseLimit(t *testing.T) {
	body := strings.Repeat("x", capturepkg.MaxBody+20)
	m, err := replaypkg.ResponseMessage(&http.Response{Proto: "HTTP/1.1", Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))})
	if err != nil || !m.Truncated || len(m.RawBody) != capturepkg.MaxBody || len(m.Body) != capturepkg.MaxBody {
		t.Fatal(fmt.Sprint("response preview unbounded: ", err))
	}
}
