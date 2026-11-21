package main

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func testIPv4() []byte {
	payload := []byte("GET / HTTP/1.1\r\n\r\n")
	p := make([]byte, 40+len(payload))
	p[0] = 0x45
	p[9] = 6
	binary.BigEndian.PutUint16(p[2:4], uint16(len(p)))
	copy(p[12:16], []byte{127, 0, 0, 1})
	copy(p[16:20], []byte{127, 0, 0, 1})
	binary.BigEndian.PutUint16(p[20:22], 12345)
	binary.BigEndian.PutUint16(p[22:24], 8080)
	p[32] = 0x50
	p[33] = 0x18
	copy(p[40:], payload)
	return p
}

func TestNormalizeCaptureHeaders(t *testing.T) {
	ip := testIPv4()
	eth := make([]byte, 14)
	binary.BigEndian.PutUint16(eth[12:14], 0x0800)
	vlan := make([]byte, 18)
	binary.BigEndian.PutUint16(vlan[12:14], 0x8100)
	binary.BigEndian.PutUint16(vlan[16:18], 0x0800)
	loop := []byte{2, 0, 0, 0}
	for _, tt := range []struct {
		name   string
		link   int
		prefix []byte
	}{{"ethernet", 1, eth}, {"vlan", 1, vlan}, {"loopback", 0, loop}, {"raw", 12, nil}} {
		t.Run(tt.name, func(t *testing.T) {
			raw := append(append([]byte{}, tt.prefix...), ip...)
			got, err := normalizeIP(raw, tt.link)
			if err != nil || !bytes.Equal(got, ip) {
				t.Fatalf("normalize: %x %v", got, err)
			}
			_, payload, err := describeTCP(got)
			if err != nil || !bytes.Equal(payload, ip[40:]) {
				t.Fatalf("TCP payload: %q %v", payload, err)
			}
		})
	}
}

func TestMalformedPackets(t *testing.T) {
	for n := 0; n < 40; n++ {
		if _, _, err := describeTCP(testIPv4()[:n]); err == nil {
			t.Fatalf("accepted truncation %d", n)
		}
	}
	for _, tt := range []struct {
		link int
		data []byte
	}{{1, make([]byte, 13)}, {0, []byte{2, 0}}, {99, testIPv4()}, {1, make([]byte, 14)}} {
		if _, err := normalizeIP(tt.data, tt.link); err == nil {
			t.Fatalf("accepted malformed link %d", tt.link)
		}
	}
	ip := testIPv4()
	ip[6] = 0x20
	if _, _, err := describeTCP(ip); err == nil {
		t.Fatal("accepted fragment without reassembly")
	}
}

func TestIPv6TCP(t *testing.T) {
	p := make([]byte, 60)
	p[0] = 0x60
	p[6] = 6
	binary.BigEndian.PutUint16(p[4:6], 20)
	p[23] = 1
	p[39] = 1
	p[52] = 0x50
	if _, _, err := describeTCP(p); err != nil {
		t.Fatal(err)
	}
}
