package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/netip"
	"time"
)

// The common processor knows only this interface, not Npcap or its link types.
type PacketSource interface {
	ReadPacket(context.Context) (IPPacket, error)
	Close() error
}
type IPPacket struct {
	Data      []byte
	Timestamp time.Time
	DeviceID  string
}

func describeTCP(ip []byte) (string, []byte, error) {
	if len(ip) == 0 {
		return "", nil, fmt.Errorf("empty IP packet")
	}
	var src, dst netip.Addr
	var tcp []byte
	switch ip[0] >> 4 {
	case 4:
		if len(ip) < 20 {
			return "", nil, fmt.Errorf("short IPv4 header")
		}
		n := int(ip[0]&15) * 4
		total := int(binary.BigEndian.Uint16(ip[2:4]))
		if n < 20 || total < n || total > len(ip) || ip[9] != 6 {
			return "", nil, fmt.Errorf("invalid/non-TCP IPv4 packet")
		}
		if binary.BigEndian.Uint16(ip[6:8])&0x3fff != 0 {
			return "", nil, fmt.Errorf("IP fragments require reassembly; not implemented in probe")
		}
		src = netip.AddrFrom4([4]byte(ip[12:16]))
		dst = netip.AddrFrom4([4]byte(ip[16:20]))
		tcp = ip[n:total]
	case 6:
		if len(ip) < 40 {
			return "", nil, fmt.Errorf("short IPv6 header")
		}
		total := 40 + int(binary.BigEndian.Uint16(ip[4:6]))
		if total > len(ip) {
			return "", nil, fmt.Errorf("truncated IPv6 packet")
		}
		src = netip.AddrFrom16([16]byte(ip[8:24]))
		dst = netip.AddrFrom16([16]byte(ip[24:40]))
		next, offset := ip[6], 40
		for next != 6 {
			if offset+2 > total {
				return "", nil, fmt.Errorf("short IPv6 extension")
			}
			var n int
			switch next {
			case 0, 43, 60:
				n = (int(ip[offset+1]) + 1) * 8
			case 51:
				n = (int(ip[offset+1]) + 2) * 4
			default:
				return "", nil, fmt.Errorf("IPv6 protocol/extension %d unsupported in probe", next)
			}
			if offset+n > total {
				return "", nil, fmt.Errorf("truncated IPv6 extension")
			}
			next = ip[offset]
			offset += n
		}
		tcp = ip[offset:total]
	default:
		return "", nil, fmt.Errorf("invalid IP version")
	}
	if len(tcp) < 20 {
		return "", nil, fmt.Errorf("short TCP header")
	}
	n := int(tcp[12]>>4) * 4
	if n < 20 || n > len(tcp) {
		return "", nil, fmt.Errorf("invalid TCP header length")
	}
	summary := fmt.Sprintf("%s -> %s seq=%d ack=%d flags=0x%02x", netip.AddrPortFrom(src, binary.BigEndian.Uint16(tcp[:2])), netip.AddrPortFrom(dst, binary.BigEndian.Uint16(tcp[2:4])), binary.BigEndian.Uint32(tcp[4:8]), binary.BigEndian.Uint32(tcp[8:12]), tcp[13])
	return summary, tcp[n:], nil
}
