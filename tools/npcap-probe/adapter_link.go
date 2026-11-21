package main

import (
	"encoding/binary"
	"fmt"
)

func supportedLink(link int) bool {
	return link == 0 || link == 1 || link == 12 || link == 101 || link == 108 || link == 228 || link == 229
}

// Adapter helper: remove capture-specific headers before handing packets to the core.
func normalizeIP(raw []byte, link int) ([]byte, error) {
	switch link {
	case 1:
		if len(raw) < 14 {
			return nil, fmt.Errorf("short Ethernet frame")
		}
		kind := binary.BigEndian.Uint16(raw[12:14])
		offset := 14
		for kind == 0x8100 || kind == 0x88a8 || kind == 0x9100 {
			if len(raw) < offset+4 {
				return nil, fmt.Errorf("short VLAN header")
			}
			kind = binary.BigEndian.Uint16(raw[offset+2 : offset+4])
			offset += 4
		}
		if kind != 0x0800 && kind != 0x86dd {
			return nil, fmt.Errorf("non-IP Ethernet type 0x%x", kind)
		}
		raw = raw[offset:]
	case 0, 108:
		if len(raw) < 4 {
			return nil, fmt.Errorf("short loopback header")
		}
		family := binary.LittleEndian.Uint32(raw[:4])
		if link == 108 {
			family = binary.BigEndian.Uint32(raw[:4])
		}
		if family != 2 && family != 24 && family != 28 && family != 30 {
			return nil, fmt.Errorf("unsupported loopback address family %d", family)
		}
		raw = raw[4:]
	case 12, 101, 228, 229:
	default:
		return nil, fmt.Errorf("unsupported link type %d", link)
	}
	if len(raw) == 0 || raw[0]>>4 != 4 && raw[0]>>4 != 6 {
		return nil, fmt.Errorf("invalid IP version")
	}
	return raw, nil
}
