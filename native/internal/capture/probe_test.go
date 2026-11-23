package capture

import (
	"net"
	"testing"
)

func probeListener(t *testing.T, reply []byte) (uint16, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write(reply)
	}()
	return uint16(ln.Addr().(*net.TCPAddr).Port), func() { _ = ln.Close() }
}

func TestProbeMySQLGreeting(t *testing.T) {
	port, stop := probeListener(t, []byte(mysqlPacket(0, mysqlGreeting())))
	defer stop()
	if !ProbeMySQL(port) {
		t.Fatal("MySQL greeting was not detected")
	}
}

func TestProbeMySQLRejectsHTTP(t *testing.T) {
	port, stop := probeListener(t, []byte("HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\n\r\n"))
	defer stop()
	if ProbeMySQL(port) {
		t.Fatal("HTTP response was misdetected as MySQL")
	}
}

func TestProbeMySQLClosedPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if ProbeMySQL(port) {
		t.Fatal("closed port was misdetected as MySQL")
	}
}
