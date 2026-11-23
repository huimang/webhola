package capture

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"sync"
	"sync/atomic"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	"github.com/gopacket/gopacket/pcapgo"
)

type fileSource struct {
	file   *os.File
	read   func() ([]byte, gopacket.CaptureInfo, error)
	link   int
	mixed  bool
	closed atomic.Bool
	once   sync.Once
	sourceCounters
}

func OpenFile(path string) (PacketSource, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	s := &fileSource{file: f}
	r := bufio.NewReader(f)
	magic, err := r.Peek(4)
	if err != nil {
		f.Close()
		return nil, err
	}
	if binary.LittleEndian.Uint32(magic) == 0x0a0d0d0a {
		o := pcapgo.DefaultNgReaderOptions
		o.WantMixedLinkType = true
		var ng *pcapgo.NgReader
		ng, err = pcapgo.NewNgReader(r, o)
		if err == nil {
			s.read = ng.ReadPacketData
			s.mixed = true
		}
	} else {
		var p *pcapgo.Reader
		p, err = pcapgo.NewReader(r)
		if err == nil {
			s.read = p.ReadPacketData
			s.link = int(p.LinkType())
			if !supportedLink(s.link) {
				err = fmt.Errorf("unsupported capture link type %d", s.link)
			}
		}
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return s, nil
}
func (s *fileSource) Close() error {
	s.once.Do(func() { s.closed.Store(true); s.file.Close() })
	return nil
}
func (s *fileSource) Stats() (CaptureStats, error) { return s.snapshot(), nil }
func (s *fileSource) ReadPacket(ctx context.Context) (IPPacket, error) {
	for {
		if err := ctx.Err(); err != nil {
			return IPPacket{}, err
		}
		if s.closed.Load() {
			return IPPacket{}, ErrClosed
		}
		raw, ci, err := s.read()
		if err != nil {
			return IPPacket{}, err
		}
		link := s.link
		if s.mixed {
			link = int(ci.AncillaryData[0].(layers.LinkType))
		}
		if !supportedLink(link) {
			return IPPacket{}, fmt.Errorf("unsupported capture link type %d", link)
		}
		if ci.CaptureLength != ci.Length {
			s.invalid.Add(1)
			continue
		}
		ip, err := normalizeIP(raw, link)
		if err != nil {
			s.invalid.Add(1)
			continue
		}
		s.received.Add(1)
		return IPPacket{Data: append([]byte(nil), ip...), Timestamp: ci.Timestamp, InterfaceID: fmt.Sprint(ci.InterfaceIndex), InterfaceIndex: ci.InterfaceIndex}, nil
	}
}
