package capture

import (
	"encoding/binary"
	"fmt"
	"time"
)

// Darwin's bpf_hdr uses a pair of 32-bit timeval fields on amd64 and arm64.
// Records use bh_hdrlen and BPF_WORDALIGN (4), not unsafe.Sizeof(BpfHdr).
func splitBPFRecord(buf []byte) (frame, rest []byte, stamp time.Time, err error) {
	if len(buf) < 18 {
		return nil, nil, stamp, fmt.Errorf("short BPF header")
	}
	sec, usec := int32(binary.LittleEndian.Uint32(buf)), int32(binary.LittleEndian.Uint32(buf[4:]))
	caplen, length := uint64(binary.LittleEndian.Uint32(buf[8:])), uint64(binary.LittleEndian.Uint32(buf[12:]))
	hdr := uint64(binary.LittleEndian.Uint16(buf[16:]))
	end := hdr + caplen
	if hdr < 18 || end > uint64(len(buf)) {
		return nil, nil, stamp, fmt.Errorf("invalid BPF record length")
	}
	next := (end + 3) &^ uint64(3)
	if next > uint64(len(buf)) {
		next = end
	}
	rest = buf[next:]
	stamp = time.Unix(int64(sec), int64(usec)*1000)
	if caplen != length {
		return nil, rest, stamp, fmt.Errorf("truncated BPF frame")
	}
	return buf[hdr:end], rest, stamp, nil
}
