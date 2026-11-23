package capture

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	"github.com/gopacket/gopacket/reassembly"
)

const (
	maxConnections   = 256
	maxFragments     = 128
	maxFragmentBytes = 8 << 20
	fragmentTTL      = 30 * time.Second
	streamIdle       = 2 * time.Minute
)

type Diagnostics struct {
	InvalidIP, FragmentDrops, TCPGaps, ParseErrors, ResourceDrops, Midstream uint64
}

func ipv4Length(raw []byte) int {
	if len(raw) < 20 || raw[0]>>4 != 4 {
		return -1
	}
	return int(binary.BigEndian.Uint16(raw[2:4]))
}

func repairIPv4Length(raw []byte) ([]byte, bool) {
	total := ipv4Length(raw)
	if total > 0 && total <= len(raw) || len(raw) > 65535 || len(raw) < 20 {
		return nil, false
	}
	if binary.BigEndian.Uint16(raw[6:8])&0x3fff != 0 {
		return nil, false
	}
	fixed := append([]byte(nil), raw...)
	binary.BigEndian.PutUint16(fixed[2:4], uint16(len(fixed)))
	return fixed, true
}

// captureGaps watches raw TCP sequence numbers. The reassembler withholds data
// while it waits for a missing segment, so a capture gap otherwise produces no
// callback and no diagnostic at all.
type captureGaps struct {
	logf  func(string, ...any)
	flows map[string]*captureFlow
}

type captureFlow struct {
	seen [2]bool
	next [2]uint32
	log  [2]bool
}

func newCaptureGaps(logf func(string, ...any)) *captureGaps {
	return &captureGaps{logf: logf, flows: map[string]*captureFlow{}}
}

func (g *captureGaps) observe(scope string, n gopacket.Flow, tcp *layers.TCP) {
	if g.logf == nil {
		return
	}
	src := net.JoinHostPort(n.Src().String(), tcp.TransportFlow().Src().String())
	dst := net.JoinHostPort(n.Dst().String(), tcp.TransportFlow().Dst().String())
	dir := 0
	if src > dst {
		dir = 1
	}
	key := scope + "|" + src + "|" + dst
	f := g.flows[key]
	if f == nil {
		if len(g.flows) >= 1024 {
			g.flows = map[string]*captureFlow{}
		}
		f = &captureFlow{}
		g.flows[key] = f
	}
	if tcp.SYN {
		f.seen[dir], f.next[dir], f.log[dir] = true, tcp.Seq+1, false
		if !tcp.ACK {
			f.seen[1-dir], f.log[1-dir] = false, false
		}
		return
	}
	length := uint32(len(tcp.Payload))
	if length == 0 {
		return
	}
	if !f.seen[dir] {
		f.seen[dir], f.next[dir] = true, tcp.Seq+length
		return
	}
	diff := int32(tcp.Seq - f.next[dir])
	switch {
	case diff == 0:
		f.next[dir] = tcp.Seq + length
	case diff > 0:
		if !f.log[dir] {
			g.logf("tcp sequence jump %s -> %s: missing_or_reordered=%d expected=%d got=%d", src, dst, diff, f.next[dir], tcp.Seq)
			f.log[dir] = true
		}
		f.next[dir] = tcp.Seq + length
	default:
		if int32(tcp.Seq+length-f.next[dir]) > 0 {
			f.next[dir] = tcp.Seq + length
		}
	}
}

type fragmentKey struct {
	scope, src, dst string
	id              uint32
	protocol        byte
}
type fragmentSet struct {
	seen         time.Time
	parts        map[int][]byte
	header       []byte
	previous     int
	total, bytes int
	bad          bool
}
type packetDecoder struct {
	sets  map[fragmentKey]*fragmentSet
	bytes int
	diag  *diagnosticCounters
}

// Only normalized IP packets enter this layer. No link types or platform checks.
func (d *packetDecoder) decode(pkt IPPacket) ([]byte, error) {
	raw := pkt.Data
	if len(raw) < 1 {
		return nil, fmt.Errorf("empty IP packet")
	}
	var offset int
	var more bool
	var key fragmentKey
	var header []byte
	previous := 0
	key.scope = pkt.InterfaceID
	switch raw[0] >> 4 {
	case 4:
		if len(raw) < 20 {
			return nil, fmt.Errorf("short IPv4 header")
		}
		h := int(raw[0]&15) * 4
		n := int(binary.BigEndian.Uint16(raw[2:4]))
		flags := binary.BigEndian.Uint16(raw[6:8])
		if (n == 0 || n > len(raw)) && flags&0x3fff == 0 {
			// Windows TCP offload (LSO) frames can report an IPv4 total
			// length of zero or a value larger than the captured IP bytes.
			// The capture itself is complete, so trust the captured length.
			n = len(raw)
			binary.BigEndian.PutUint16(raw[2:4], uint16(n))
		}
		if h < 20 || n < h || len(raw) < n {
			return nil, fmt.Errorf("truncated IPv4 packet: captured=%d declared=%d flags=%#x", len(raw), n, flags)
		}
		raw = raw[:n]
		offset = int(flags&0x1fff) * 8
		more = flags&0x2000 != 0
		if offset == 0 && !more {
			return raw, nil
		}
		key.src = string(raw[12:16])
		key.dst = string(raw[16:20])
		key.id = uint32(binary.BigEndian.Uint16(raw[4:6]))
		key.protocol = raw[9]
		header = raw[:h]
		raw = raw[h:]
	case 6:
		if len(raw) < 40 {
			return nil, fmt.Errorf("short IPv6 header")
		}
		n := 40 + int(binary.BigEndian.Uint16(raw[4:6]))
		if n == 40 || len(raw) < n {
			return nil, fmt.Errorf("truncated IPv6 packet or unsupported jumbogram")
		}
		raw = raw[:n]
		next, pos, prev := raw[6], 40, 6
		for hops := 0; hops < 16; hops++ {
			if next == 44 {
				if pos+8 > len(raw) {
					return nil, fmt.Errorf("short IPv6 fragment header")
				}
				bits := binary.BigEndian.Uint16(raw[pos+2:])
				offset = int(bits & 0xfff8)
				more = bits&1 != 0
				key.src = string(raw[8:24])
				key.dst = string(raw[24:40])
				key.id = binary.BigEndian.Uint32(raw[pos+4:])
				key.protocol = raw[pos]
				header = append([]byte(nil), raw[:pos]...)
				header[prev] = raw[pos]
				previous = prev
				raw = raw[pos+8:]
				break
			}
			if next != 0 && next != 43 && next != 60 && next != 51 {
				return raw, nil
			}
			if pos+2 > len(raw) {
				return nil, fmt.Errorf("short IPv6 extension header")
			}
			length := (int(raw[pos+1]) + 1) * 8
			if next == 51 {
				length = (int(raw[pos+1]) + 2) * 4
			}
			if pos+length > len(raw) {
				return nil, fmt.Errorf("truncated IPv6 extension header")
			}
			next, prev, pos = raw[pos], pos, pos+length
		}
		if header == nil {
			return nil, fmt.Errorf("too many IPv6 extension headers")
		}
	default:
		return nil, fmt.Errorf("invalid IP version")
	}
	if len(raw) == 0 || more && len(raw)%8 != 0 || offset+len(raw) > 65535 {
		return nil, fmt.Errorf("invalid IP fragment size")
	}
	if d.sets == nil {
		d.sets = map[fragmentKey]*fragmentSet{}
	}
	d.expire(pkt.Timestamp)
	s := d.sets[key]
	if s == nil {
		if len(d.sets) >= maxFragments || d.bytes+len(raw) > maxFragmentBytes {
			d.diag.fragmentDrops.Add(1)
			return nil, nil
		}
		s = &fragmentSet{seen: pkt.Timestamp, parts: map[int][]byte{}, total: -1}
		d.sets[key] = s
	}
	if s.bad {
		return nil, nil
	}
	if pkt.Timestamp.After(s.seen) {
		s.seen = pkt.Timestamp
	}
	bad := func() {
		d.bytes -= s.bytes
		s.bytes = 0
		s.parts = nil
		s.header = nil
		s.bad = true
		d.diag.fragmentDrops.Add(1)
	}
	for off, part := range s.parts {
		if off == offset && bytes.Equal(part, raw) {
			return nil, nil
		}
		if offset < off+len(part) && off < offset+len(raw) {
			bad()
			return nil, nil
		}
	}
	if d.bytes+len(raw) > maxFragmentBytes || len(s.parts) >= 256 {
		bad()
		return nil, nil
	}
	if !more {
		if s.total >= 0 && s.total != offset+len(raw) {
			bad()
			return nil, nil
		}
		s.total = offset + len(raw)
	}
	if s.total >= 0 && offset+len(raw) > s.total {
		bad()
		return nil, nil
	}
	if offset == 0 {
		s.header = append([]byte(nil), header...)
		s.previous = previous
	}
	s.parts[offset] = append([]byte(nil), raw...)
	s.bytes += len(raw)
	d.bytes += len(raw)
	if s.header == nil || s.total < 0 {
		return nil, nil
	}
	pos := 0
	for pos < s.total {
		p, ok := s.parts[pos]
		if !ok {
			return nil, nil
		}
		pos += len(p)
	}
	if pos != s.total {
		bad()
		return nil, nil
	}
	out := append([]byte(nil), s.header...)
	for off := 0; off < s.total; {
		p := s.parts[off]
		out = append(out, p...)
		off += len(p)
	}
	delete(d.sets, key)
	d.bytes -= s.bytes
	if out[0]>>4 == 4 {
		if len(out) > 65535 {
			return nil, fmt.Errorf("IPv4 reassembly too large")
		}
		binary.BigEndian.PutUint16(out[2:4], uint16(len(out)))
		binary.BigEndian.PutUint16(out[6:8], 0)
		out[10], out[11] = 0, 0
	} else {
		if len(out)-40 > 65535 {
			return nil, fmt.Errorf("IPv6 reassembly too large")
		}
		binary.BigEndian.PutUint16(out[4:6], uint16(len(out)-40))
	}
	return out, nil
}
func (d *packetDecoder) expire(now time.Time) {
	for key, s := range d.sets {
		if now.Sub(s.seen) > fragmentTTL {
			d.bytes -= s.bytes
			delete(d.sets, key)
			d.diag.fragmentDrops.Add(1)
		}
	}
}

type packetContext struct{ ci gopacket.CaptureInfo }

func (c packetContext) GetCaptureInfo() gopacket.CaptureInfo { return c.ci }

// Process owns the protocol workers and serializes immutable emissions. All
// caches have explicit limits. A gap/parse error terminates that connection;
// capture that starts after SYN is deliberately not guessed back into HTTP.
func Process(ctx context.Context, source PacketSource, cfg CaptureConfig, emit func(Exchange)) (Diagnostics, error) {
	if cfg.TCPPort == 0 {
		return Diagnostics{}, fmt.Errorf("invalid capture port")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	diag := &diagnosticCounters{}
	factory := newStreamFactory(ctx, emit, diag, cfg.Logf)
	factory.mysqlPort = cfg.MySQL || cfg.TCPPort == mysqlDefaultPort
	gaps := newCaptureGaps(cfg.Logf)
	decoder := packetDecoder{diag: diag}
	assemblers := map[string]*reassembly.Assembler{}
	// Closing the source is part of cancellation, even while ReadPacket blocks.
	stopClose := context.AfterFunc(ctx, func() { source.Close() })
	defer stopClose()
	last := time.Time{}
	var result error
	invalidLogged := 0
	logInvalid := func(format string, args ...any) {
		if cfg.Logf != nil && invalidLogged < 20 {
			cfg.Logf(format, args...)
			invalidLogged++
		}
	}
	for {
		pkt, err := source.ReadPacket(ctx)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				result = err
			}
			break
		}
		if pkt.Timestamp.After(last) {
			last = pkt.Timestamp
		}
		raw, err := decoder.decode(pkt)
		if err != nil {
			diag.invalidIP.Add(1)
			logInvalid("invalid packet len=%d: %v", len(pkt.Data), err)
			continue
		}
		if raw == nil {
			continue
		}
		layer := layers.LayerTypeIPv4
		if raw[0]>>4 == 6 {
			layer = layers.LayerTypeIPv6
		}
		p := gopacket.NewPacket(raw, layer, gopacket.DecodeOptions{Lazy: false, NoCopy: true})
		if p.ErrorLayer() != nil || p.NetworkLayer() == nil {
			if p.ErrorLayer() != nil {
				logInvalid("invalid packet len=%d ip_len=%d: %v", len(raw), ipv4Length(raw), p.ErrorLayer().Error())
			} else {
				logInvalid("invalid packet len=%d: no network layer", len(raw))
			}
			// Some Wi-Fi offload/coalescing frames report an IPv4 total length
			// larger than the captured IP bytes, which gopacket rejects as
			// truncated. Trust the captured length when the difference is small.
			fixed, ok := repairIPv4Length(raw)
			if !ok {
				diag.invalidIP.Add(1)
				continue
			}
			p = gopacket.NewPacket(fixed, layer, gopacket.DecodeOptions{Lazy: false, NoCopy: true})
			if p.ErrorLayer() != nil || p.NetworkLayer() == nil {
				diag.invalidIP.Add(1)
				continue
			}
			logInvalid("repaired packet len=%d ip_len=%d", len(fixed), ipv4Length(fixed))
		}
		tcp, ok := p.TransportLayer().(*layers.TCP)
		if !ok {
			continue
		}
		if tcp.SrcPort != layers.TCPPort(cfg.TCPPort) && tcp.DstPort != layers.TCPPort(cfg.TCPPort) {
			continue
		}
		gaps.observe(pkt.InterfaceID, p.NetworkLayer().NetworkFlow(), tcp)
		key := flowKey(pkt.InterfaceID, p.NetworkLayer().NetworkFlow(), tcp.TransportFlow())
		if !factory.hasRoom(key) {
			diag.resourceDrops.Add(1)
			factory.log("drop stream %s: too many active connections", key)
			continue
		}
		a := assemblers[pkt.InterfaceID]
		if a == nil {
			if len(assemblers) >= 32 {
				diag.resourceDrops.Add(1)
				continue
			}
			factory.scope = pkt.InterfaceID
			a = reassembly.NewAssembler(reassembly.NewStreamPool(factory))
			a.MaxBufferedPagesPerConnection = 128
			a.MaxBufferedPagesTotal = 4096
			assemblers[pkt.InterfaceID] = a
		}
		factory.scope = pkt.InterfaceID
		ci := gopacket.CaptureInfo{Timestamp: pkt.Timestamp, CaptureLength: len(raw), Length: len(raw), InterfaceIndex: pkt.InterfaceIndex}
		a.AssembleWithContext(p.NetworkLayer().NetworkFlow(), tcp, packetContext{ci})
		// Capture time, not wall time, also makes historical files deterministic.
		if factory.lastFlush.IsZero() || last.Sub(factory.lastFlush) >= time.Second {
			for _, asm := range assemblers {
				asm.FlushCloseOlderThan(last.Add(-streamIdle))
			}
			decoder.expire(last)
			factory.lastFlush = last
		}
	}
	for _, a := range assemblers {
		a.FlushAll()
	}
	factory.wait()
	return diag.snapshot(), result
}
