package jvm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	i18npkg "webhola/native/internal/i18n"
)

func TestArthasMetaspaceCheck(t *testing.T) {
	for _, tc := range []struct {
		Name, Output string
		Blocked      bool
	}{
		{"exhausted", "Both: 127.20 MB used.\nMaxMetaspaceSize: 128.00 MB\n", true},
		{"kilobytes", "Both: 130252.80 KB used.\nMaxMetaspaceSize: 131072.00 KB\n", true},
		{"large heap", "Both: 1.99 GB used.\nMaxMetaspaceSize: 2.00 GB\n", true},
		{"room", "Both: 127.20 MB used.\nMaxMetaspaceSize: 256.00 MB\n", false},
		{"boundary", "Both: 112.00 MB used.\nMaxMetaspaceSize: 128.00 MB\n", false},
		{"unlimited", "Both: 127.20 MB used.\nMaxMetaspaceSize: unlimited\n", false},
		{"permission denied", "Access is denied.", false},
		{"missing usage", "MaxMetaspaceSize: 128.00 MB\n", false},
	} {
		t.Run(tc.Name, func(t *testing.T) {
			err := arthasMetaspaceCheck(10700, tc.Output)
			if (err != nil) != tc.Blocked {
				t.Fatalf("unexpected check result: %v", err)
			}
			if err != nil {
				for _, lang := range []string{"zh-CN", "en"} {
					text := i18npkg.TranslateText(lang, i18npkg.ErrorMessage(err))
					if !strings.Contains(text, "10700") || !strings.Contains(text, "-XX:MaxMetaspaceSize=") || strings.Contains(text, "%!") {
						t.Fatal(text)
					}
				}
			}
		})
	}
}

func serveArthasWelcome(w http.ResponseWriter, r *http.Request, pid string) {
	var payload map[string]any
	_ = json.NewDecoder(r.Body).Decode(&payload)
	switch payload["action"] {
	case "init_session":
		fmt.Fprint(w, `{"state":"SUCCEEDED","sessionId":"s","consumerId":"c"}`)
	case "pull_results":
		fmt.Fprintf(w, `{"state":"SUCCEEDED","body":{"results":[{"type":"welcome","pid":"%s"}]}}`, pid)
	default:
		fmt.Fprint(w, `{"state":"SUCCEEDED"}`)
	}
}

func TestArthasWaitReady(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serveArthasWelcome(w, r, "10700") }))
	// A bound but not yet serving listener simulates delayed HTTP startup.
	c := newArthasClient(server.Listener.Addr().(*net.TCPAddr).Port, 10700)
	defer c.Http.CloseIdleConnections()
	started := make(chan struct{})
	go func() { time.Sleep(150 * time.Millisecond); server.Start(); close(started) }()
	defer func() { <-started; server.Close() }()
	if err := c.waitReady(context.Background(), 2*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestArthasWaitReadyRejectsOtherPID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serveArthasWelcome(w, r, "999") }))
	defer server.Close()
	c := newArthasClient(1, 10700)
	c.Endpoint = server.URL
	defer c.Http.CloseIdleConnections()
	err := c.waitReady(context.Background(), time.Second)
	if err == nil || !strings.Contains(i18npkg.TranslateText("en", i18npkg.ErrorMessage(err)), "999") {
		t.Fatal(err)
	}
}

func TestArthasReadinessCancellationAndTimeout(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	c := newArthasClient(port, 10700)
	defer c.Http.CloseIdleConnections()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.waitReady(ctx, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := c.waitReady(context.Background(), 100*time.Millisecond); err == nil {
		t.Fatal("unavailable endpoint accepted")
	}
	if err := checkArthasMetaspace(ctx, "missing-java", 10700); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := checkArthasMetaspace(context.Background(), "missing-folder/java", 10700); err != nil {
		t.Fatalf("missing jcmd blocked attach: %v", err)
	}
}
