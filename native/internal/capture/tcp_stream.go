package capture

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	"github.com/gopacket/gopacket/reassembly"
)

const maxQueuedBytes = 8 << 20
const maxQueuedTotal = 64 << 20

var errStreamAborted = errors.New("incomplete captured TCP stream")

type diagnosticCounters struct{ invalidIP, fragmentDrops, tcpGaps, parseErrors, resourceDrops, midstream atomic.Uint64 }

func (d *diagnosticCounters) snapshot() Diagnostics {
	return Diagnostics{InvalidIP: d.invalidIP.Load(), FragmentDrops: d.fragmentDrops.Load(), TCPGaps: d.tcpGaps.Load(), ParseErrors: d.parseErrors.Load(), ResourceDrops: d.resourceDrops.Load(), Midstream: d.midstream.Load()}
}

type streamFactory struct {
	ctx       context.Context
	scope     string // Set only by the assembler owner before each Assemble call.
	active    map[string]*httpStream
	wg        sync.WaitGroup
	emitMu    sync.Mutex
	emit      func(Exchange)
	next      atomic.Uint64
	streams   atomic.Uint64
	queued    atomic.Int64
	diag      *diagnosticCounters
	logf      func(string, ...any)
	lastFlush time.Time
	// mysqlPort is set when the capture port is MySQL's default, which takes
	// precedence over greeting detection.
	mysqlPort bool
	// notify wakes protocol detection whenever either side receives bytes.
	notify chan struct{}
}

func newStreamFactory(ctx context.Context, emit func(Exchange), diag *diagnosticCounters, logf func(string, ...any)) *streamFactory {
	return &streamFactory{ctx: ctx, emit: emit, diag: diag, logf: logf, active: map[string]*httpStream{}, notify: make(chan struct{}, 1)}
}

func (f *streamFactory) log(format string, args ...any) {
	if f.logf != nil {
		f.logf(format, args...)
	}
}
func flowKey(scope string, n, t gopacket.Flow) string {
	a, b := net.JoinHostPort(n.Src().String(), t.Src().String()), net.JoinHostPort(n.Dst().String(), t.Dst().String())
	if a > b {
		a, b = b, a
	}
	return scope + "|" + a + "|" + b
}
func (f *streamFactory) hasRoom(key string) bool {
	return f.active[key] != nil || len(f.active) < maxConnections
}
func (f *streamFactory) publish(e Exchange) {
	f.emitMu.Lock()
	defer f.emitMu.Unlock()
	if f.emit != nil && f.ctx.Err() == nil {
		f.emit(e)
	}
}
func (f *streamFactory) wait() { f.wg.Wait() }
func (f *streamFactory) New(n, t gopacket.Flow, tcp *layers.TCP, ac reassembly.AssemblerContext) reassembly.Stream {
	s := &httpStream{f: f, key: flowKey(f.scope, n, t), id: f.streams.Add(1), client: net.JoinHostPort(n.Src().String(), t.Src().String()), server: net.JoinHostPort(n.Dst().String(), t.Dst().String()), requests: make(chan *capturedRequest, MaxEntries)}
	s.input[0] = newStreamInput(f)
	s.input[1] = newStreamInput(f)
	f.active[s.key] = s
	if !tcp.SYN || tcp.ACK {
		s.midstream = true
		f.diag.midstream.Add(1)
		f.log("take over midstream stream=%d %s -> %s SYN=%v ACK=%v", s.id, s.client, s.server, tcp.SYN, tcp.ACK)
	}
	f.wg.Add(2)
	go func() { defer f.wg.Done(); defer close(s.requests); s.parseClient() }()
	go func() { defer f.wg.Done(); s.parseServer() }()
	return s
}

type httpStream struct {
	f                   *streamFactory
	id                  uint64
	key, client, server string
	input               [2]*streamInput
	requests            chan *capturedRequest
	midstream           bool
	aborted             atomic.Bool
	once                sync.Once
	// Protocol decision is shared by both parser goroutines.
	proto     protocolMode
	protoOnce sync.Once
	// MySQL connection state. Capabilities are written by the client parser and
	// read by the response parser.
	mysqlCaps       atomic.Uint32
	mysqlStop       atomic.Bool
	mysqlStatements sync.Map // statement id (uint32) -> SQL
	mysqlPending    *mysqlFrame
}

func (s *httpStream) abort() { s.abortWith(false) }

// abortWith terminates the stream. Data already captured is parsed unless the
// caller is shedding load, so a complete request before a reset is still shown.
func (s *httpStream) abortWith(discard bool) {
	s.once.Do(func() {
		s.aborted.Store(true)
		for _, r := range s.input {
			r.finish(errStreamAborted, discard)
		}
	})
}
func (s *httpStream) Accept(tcp *layers.TCP, ci gopacket.CaptureInfo, dir reassembly.TCPFlowDirection, next reassembly.Sequence, start *bool, ac reassembly.AssemblerContext) bool {
	if tcp.RST {
		s.f.log("rst stream=%d %s -> %s", s.id, s.client, s.server)
		s.abort()
		return false
	}
	if s.midstream {
		// No SYN was captured. Start the half-connection at the first segment
		// seen so later contiguous data can still be parsed.
		*start = true
	}
	return !s.aborted.Load()
}
func (s *httpStream) ReassembledSG(sg reassembly.ScatterGather, ac reassembly.AssemblerContext) {
	if s.aborted.Load() {
		return
	}
	dir, _, end, skip := sg.Info()
	if skip != 0 {
		s.f.diag.tcpGaps.Add(1)
		s.f.log("tcp gap stream=%d %s -> %s skip=%d", s.id, s.client, s.server, skip)
		s.abort()
		return
	}
	i := 0
	if dir == reassembly.TCPDirServerToClient {
		i = 1
	}
	n, _ := sg.Lengths()
	if n > 0 && !s.input[i].push(sg.Fetch(n), sg.CaptureInfo(n-1).Timestamp) {
		s.f.diag.resourceDrops.Add(1)
		s.f.log("stream buffer full stream=%d %s -> %s", s.id, s.client, s.server)
		s.abortWith(true)
		return
	}
	if end {
		s.input[i].finish(io.EOF, false)
	}
}
func (s *httpStream) ReassemblyComplete(ac reassembly.AssemblerContext) bool {
	for _, r := range s.input {
		r.finish(errStreamAborted, false)
	}
	delete(s.f.active, s.key)
	return true
}

type timedChunk struct {
	data  []byte
	stamp time.Time
}
type timeSpan struct {
	end   int64
	stamp time.Time
}

// Bounded producer/consumer input. The assembler never waits for HTTP parsing;
// excess queued bytes terminate the connection instead of growing without bound.
type streamInput struct {
	mu    sync.Mutex
	cond  *sync.Cond
	f     *streamFactory
	queue []timedChunk
	bytes int
	err   error
	// The remaining fields belong exclusively to the parser goroutine.
	read         int64
	spans        []timeSpan
	headerBudget int
}

func newStreamInput(f *streamFactory) *streamInput {
	r := &streamInput{f: f, headerBudget: -1}
	r.cond = sync.NewCond(&r.mu)
	return r
}
func (r *streamInput) push(b []byte, stamp time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return false
	}
	if r.bytes+len(b) > maxQueuedBytes {
		return false
	}
	if r.f.queued.Add(int64(len(b))) > maxQueuedTotal {
		r.f.queued.Add(-int64(len(b)))
		return false
	}
	r.queue = append(r.queue, timedChunk{data: append([]byte(nil), b...), stamp: stamp})
	r.bytes += len(b)
	r.cond.Signal()
	if r.f.notify != nil {
		select {
		case r.f.notify <- struct{}{}:
		default:
		}
	}
	return true
}
func (r *streamInput) finish(err error, discard bool) {
	r.mu.Lock()
	if discard {
		r.f.queued.Add(-int64(r.bytes))
		r.queue = nil
		r.bytes = 0
		r.err = err
	} else if r.err == nil {
		r.err = err
	}
	r.cond.Broadcast()
	r.mu.Unlock()
	// Wake protocol detection, which otherwise waits for first bytes forever.
	if r.f.notify != nil {
		select {
		case r.f.notify <- struct{}{}:
		default:
		}
	}
}

// exhausted reports whether the direction has ended and no bytes remain.
func (r *streamInput) exhausted() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err != nil && r.bytes == 0
}

// peekAt copies up to n queued bytes without consuming them. Protocol
// detection uses it so the selected parser still sees the first bytes.
func (r *streamInput) peekAt(n int) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]byte, 0, n)
	for _, c := range r.queue {
		need := n - len(out)
		if need > len(c.data) {
			need = len(c.data)
		}
		out = append(out, c.data[:need]...)
		if len(out) >= n {
			break
		}
	}
	return out
}
func (r *streamInput) Read(b []byte) (int, error) {
	if r.headerBudget == 0 {
		return 0, fmt.Errorf("HTTP header exceeds 64 KiB")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for r.bytes == 0 && r.err == nil && r.f.ctx.Err() == nil {
		r.cond.Wait()
	}
	if err := r.f.ctx.Err(); err != nil {
		return 0, err
	}
	if r.bytes == 0 {
		return 0, r.err
	}
	if r.headerBudget > 0 && len(b) > r.headerBudget {
		b = b[:r.headerBudget]
	}
	c := &r.queue[0]
	n := copy(b, c.data)
	c.data = c.data[n:]
	r.bytes -= n
	r.f.queued.Add(-int64(n))
	r.read += int64(n)
	r.spans = append(r.spans, timeSpan{end: r.read, stamp: c.stamp})
	if len(c.data) == 0 {
		r.queue[0] = timedChunk{}
		r.queue = r.queue[1:]
	}
	if r.headerBudget > 0 {
		r.headerBudget -= n
	}
	return n, nil
}
func (r *streamInput) timestamp(position int64) time.Time {
	for len(r.spans) > 1 && position >= r.spans[0].end {
		r.spans = r.spans[1:]
	}
	if len(r.spans) > 0 {
		return r.spans[0].stamp
	}
	return time.Time{}
}
