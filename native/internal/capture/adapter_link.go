package capture

import (
	"encoding/binary"
	"fmt"
)

func supportedLink(link int) bool {
	switch link {
	case 0, 1, 12, 101, 108, 113, 228, 229, 276:
		return true
	}
	return false
}

// normalizeIP belongs to the adapters, including the offline file adapter.
func normalizeIP(raw []byte, link int) ([]byte, error) {
	switch link {
	case 1:
		if len(raw) < 14 {
			return nil, fmt.Errorf("short Ethernet frame")
		}
		kind, off := binary.BigEndian.Uint16(raw[12:14]), 14
		for kind == 0x8100 || kind == 0x88a8 || kind == 0x9100 {
			if len(raw) < off+4 {
				return nil, fmt.Errorf("short VLAN header")
			}
			kind = binary.BigEndian.Uint16(raw[off+2 : off+4])
			off += 4
		}
		if kind != 0x800 && kind != 0x86dd {
			return nil, fmt.Errorf("non-IP Ethernet frame")
		}
		raw = raw[off:]
	case 0, 108:
		if len(raw) < 4 {
			return nil, fmt.Errorf("short loopback header")
		}
		family := binary.LittleEndian.Uint32(raw[:4])
		if link == 108 {
			family = binary.BigEndian.Uint32(raw[:4])
		}
		if family != 2 && family != 24 && family != 28 && family != 30 {
			return nil, fmt.Errorf("non-IP loopback frame")
		}
		raw = raw[4:]
	case 113:
		if len(raw) < 16 {
			return nil, fmt.Errorf("short Linux cooked frame")
		}
		kind := binary.BigEndian.Uint16(raw[14:16])
		if kind != 0x800 && kind != 0x86dd {
			return nil, fmt.Errorf("non-IP cooked frame")
		}
		raw = raw[16:]
	case 276:
		if len(raw) < 20 {
			return nil, fmt.Errorf("short Linux cooked v2 frame")
		}
		kind := binary.BigEndian.Uint16(raw[:2])
		if kind != 0x800 && kind != 0x86dd {
			return nil, fmt.Errorf("non-IP cooked frame")
		}
		raw = raw[20:]
	case 12, 101, 228, 229:
	default:
		return nil, fmt.Errorf("unsupported capture link type %d", link)
	}
	if len(raw) == 0 || raw[0]>>4 != 4 && raw[0]>>4 != 6 {
		return nil, fmt.Errorf("invalid IP version")
	}
	return raw, nil
}
