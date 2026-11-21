package desktop

import (
	"context"
	"encoding/binary"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	discoverypkg "webhola/native/internal/discovery"
)

func TestWindowsListenerTableDecoding(t *testing.T) {
	for _, v6 := range []bool{false, true} {
		stride := 24
		if v6 {
			stride = 56
		}
		b := make([]byte, 4+stride)
		binary.LittleEndian.PutUint32(b, 1)
		r := b[4:]
		if v6 {
			copy(r, net.ParseIP("::1").To16())
			binary.BigEndian.PutUint16(r[20:], 8080)
			binary.LittleEndian.PutUint32(r[52:], 123)
		} else {
			copy(r[4:], net.ParseIP("127.0.0.1").To4())
			binary.BigEndian.PutUint16(r[8:], 8080)
			binary.LittleEndian.PutUint32(r[20:], 123)
		}
		got, err := discoverypkg.DecodeListeners(b, v6)
		if err != nil || len(got) != 1 || got[0].Port != 8080 || got[0].PID != 123 || !net.ParseIP(got[0].IP).IsLoopback() {
			t.Fatalf("invalid table decode: %+v %v", got, err)
		}
		if _, err := discoverypkg.DecodeListeners(b[:len(b)-1], v6); err == nil {
			t.Fatal("truncated table accepted")
		}
	}
}

func TestWindowsDiscoveryFindsLocalHTTPListener(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer server.Close()
	expected := serverListener(t, server)
	list, err := discoverypkg.ListListeners(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var found *discoverypkg.Listener
	for i := range list {
		if list[i].Port == expected.Port && list[i].IP == expected.IP {
			found = &list[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("local listener %s:%d missing", expected.IP, expected.Port)
	}
	if found.PID != uint32(os.Getpid()) {
		t.Fatalf("wrong process: %+v", found)
	}
	services := discoverypkg.ScanListeners(context.Background(), []discoverypkg.Listener{*found}, discoverypkg.ProbeHTTP, nil)
	if len(services) != 1 || services[0].Status != 204 {
		t.Fatalf("enumerate/probe failed: %+v", services)
	}
	t.Log("confirmed local HTTP listener on port " + strconv.Itoa(expected.Port))
	if _, err := discoverypkg.LocalAdapters(); err != nil {
		t.Fatal(err)
	}
}
