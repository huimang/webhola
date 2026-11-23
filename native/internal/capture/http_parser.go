package capture

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

type capturedRequest struct {
	mu       sync.Mutex
	req      *http.Request
	exchange Exchange
	complete bool
	// MySQL command state, unused for HTTP requests.
	command   byte
	sql       string
	statement uint32
}

func (x *capturedRequest) snapshot() Exchange {
	e := x.exchange
	e.Interim = append([]int(nil), e.Interim...)
	return e
}

type previewWriter struct {
	data  []byte
	total int
}

func (w *previewWriter) Write(b []byte) (int, error) {
	n := len(b)
	w.total += n
	if left := MaxBody - len(w.data); left > 0 {
		if len(b) > left {
			b = b[:left]
		}
		w.data = append(w.data, b...)
	}
	return n, nil
}
func bodyMessage(line, headers, contentType, encoding string, body io.ReadCloser) (Message, error) {
	m := Message{Line: line, Headers: headers, ContentType: contentType}
	var r io.Reader = body
	if strings.EqualFold(strings.TrimSpace(encoding), "gzip") && body != http.NoBody {
		gz, err := gzip.NewReader(body)
		if err != nil {
			return m, err
		}
		defer gz.Close()
		r = gz
	}
	w := &previewWriter{}
	_, err := io.Copy(w, r)
	if err != nil {
		return m, err
	}
	if err = body.Close(); err != nil {
		return m, err
	}
	m.RawBody = w.data
	m.Bytes = w.total
	m.Truncated = w.total > MaxBody
	if utf8.Valid(w.data) && !bytes.ContainsRune(w.data, 0) {
		m.Body = string(w.data)
	} else {
		m.Body = hex.Dump(w.data)
	}
	return m, nil
}
func requestHeaders(r *http.Request) string {
	var b bytes.Buffer
	if r.Host != "" {
		fmt.Fprintf(&b, "Host: %s\r\n", r.Host)
	}
	r.Header.Write(&b)
	// net/http moves framing headers into fields. Preserve them for display and replay.
	if r.ContentLength > 0 && r.Header.Get("Content-Length") == "" {
		fmt.Fprintf(&b, "Content-Length: %d\r\n", r.ContentLength)
	}
	if len(r.TransferEncoding) > 0 {
		fmt.Fprintf(&b, "Transfer-Encoding: %s\r\n", strings.Join(r.TransferEncoding, ", "))
	}
	return b.String()
}
func responseHeaders(r *http.Response) string {
	var b bytes.Buffer
	r.Header.Write(&b)
	if len(r.TransferEncoding) > 0 {
		fmt.Fprintf(&b, "Transfer-Encoding: %s\r\n", strings.Join(r.TransferEncoding, ", "))
	}
	return b.String()
}
func (s *httpStream) parseError(err error) {
	if err != io.EOF && !s.aborted.Load() && s.f.ctx.Err() == nil {
		s.f.diag.parseErrors.Add(1)
		s.f.log("parse error stream=%d %s -> %s: %v", s.id, s.client, s.server, err)
	}
	s.abortWith(true)
}

// syncLines discards bytes until a line looks like an HTTP start line. It is
// used only for connections captured after they were established, where the
// first observed bytes may be the middle of an earlier message.
func syncLines(br *bufio.Reader, match func([]byte) bool) (*bufio.Reader, int, error) {
	skipped := 0
	for skipped <= 64*1024 {
		line, err := br.ReadBytes('\n')
		if match(line) {
			return bufio.NewReaderSize(io.MultiReader(bytes.NewReader(line), br), 64*1024), skipped, nil
		}
		skipped += len(line)
		if err != nil {
			return nil, skipped, err
		}
	}
	return nil, skipped, fmt.Errorf("HTTP start line not found")
}

func isRequestLine(line []byte) bool {
	line = bytes.TrimRight(line, "\r\n")
	sp := bytes.IndexByte(line, ' ')
	if sp <= 0 || len(line) < 14 {
		return false
	}
	switch string(line[:sp]) {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE":
	default:
		return false
	}
	return bytes.HasSuffix(line, []byte(" HTTP/1.1")) || bytes.HasSuffix(line, []byte(" HTTP/1.0"))
}

func isResponseLine(line []byte) bool {
	line = bytes.TrimRight(line, "\r\n")
	if len(line) < 12 {
		return false
	}
	if !bytes.HasPrefix(line, []byte("HTTP/1.1 ")) && !bytes.HasPrefix(line, []byte("HTTP/1.0 ")) {
		return false
	}
	for _, b := range line[9:12] {
		if b < '0' || b > '9' {
			return false
		}
	}
	return true
}

func (s *httpStream) parseRequests() {
	r := s.input[0]
	br := bufio.NewReaderSize(r, 64*1024)
	if s.midstream {
		var skipped int
		var err error
		br, skipped, err = syncLines(br, isRequestLine)
		if err != nil {
			if err != io.EOF {
				s.parseError(err)
			}
			return
		}
		if skipped > 0 {
			s.f.log("resync request stream=%d %s -> %s skipped=%d", s.id, s.client, s.server, skipped)
		}
	}
	for {
		// EOF/error between messages is a boundary, not a corrupt message. An
		// incomplete header or body below still terminates the connection.
		if _, err := br.Peek(1); err != nil {
			return
		}
		pos := r.read - int64(br.Buffered())
		r.headerBudget = 64*1024 - br.Buffered()
		if r.headerBudget < 0 {
			r.headerBudget = 0
		}
		req, err := http.ReadRequest(br)
		r.headerBudget = -1
		if err != nil {
			if err != io.EOF {
				s.parseError(err)
			}
			return
		}
		if r.read-int64(br.Buffered())-pos > 64*1024 || req.ProtoMajor != 1 {
			s.parseError(fmt.Errorf("invalid HTTP/1.x header"))
			return
		}
		started := r.timestamp(pos)
		url := req.URL.String()
		if !req.URL.IsAbs() {
			url = "http://" + req.Host + req.RequestURI
		}
		id := s.f.next.Add(1)
		s.f.log("request id=%d stream=%d %s %s client=%s server=%s", id, s.id, req.Method, url, s.client, s.server)
		x := &capturedRequest{req: req, exchange: Exchange{ID: id, Frame: fmt.Sprintf("request-%d", id), Stream: strconv.FormatUint(s.id, 10), Method: req.Method, URL: url, Client: s.client, Server: s.server, Started: started}}
		select {
		case s.requests <- x:
		case <-s.f.ctx.Done():
			return
		default:
			s.f.diag.resourceDrops.Add(1)
			s.abortWith(true)
			return
		}
		m, err := bodyMessage(req.Method+" "+req.RequestURI+" "+req.Proto, requestHeaders(req), req.Header.Get("Content-Type"), req.Header.Get("Content-Encoding"), req.Body)
		if err != nil {
			s.parseError(err)
			return
		}
		x.mu.Lock()
		x.exchange.Request = m
		x.complete = true
		s.f.publish(x.snapshot())
		x.mu.Unlock()
		// Prune timing metadata even for large bodies consumed in many reads.
		r.timestamp(r.read - int64(br.Buffered()) - 1)
	}
}
func (s *httpStream) parseResponses() {
	r := s.input[1]
	br := bufio.NewReaderSize(r, 64*1024)
	synced := !s.midstream
	for {
		var x *capturedRequest
		select {
		case <-s.f.ctx.Done():
			return
		case next, ok := <-s.requests:
			if !ok {
				return
			}
			x = next
		}
		if !synced {
			var skipped int
			var err error
			br, skipped, err = syncLines(br, isResponseLine)
			if err != nil {
				if err != io.EOF {
					s.parseError(err)
				}
				return
			}
			if skipped > 0 {
				s.f.log("resync response stream=%d %s -> %s skipped=%d", s.id, s.client, s.server, skipped)
			}
			synced = true
		}
		for {
			if _, err := br.Peek(1); err != nil {
				return
			}
			pos := r.read - int64(br.Buffered())
			r.headerBudget = 64*1024 - br.Buffered()
			if r.headerBudget < 0 {
				r.headerBudget = 0
			}
			res, err := http.ReadResponse(br, x.req)
			r.headerBudget = -1
			if err != nil {
				if err != io.EOF {
					s.parseError(err)
				}
				return
			}
			if res.ProtoMajor != 1 || r.read-int64(br.Buffered())-pos > 64*1024 {
				s.parseError(fmt.Errorf("invalid HTTP/1.x header"))
				return
			}
			s.f.log("response id=%d stream=%d status=%d length=%d encoding=%q transfer=%q %s",
				x.exchange.ID, s.id, res.StatusCode, res.ContentLength, res.Header.Get("Content-Encoding"),
				strings.Join(res.TransferEncoding, ","), x.exchange.URL)
			if res.StatusCode < 200 && res.StatusCode != 101 {
				res.Body.Close()
				x.mu.Lock()
				x.exchange.Interim = append(x.exchange.Interim, res.StatusCode)
				if x.complete {
					s.f.publish(x.snapshot())
				}
				x.mu.Unlock()
				continue
			}
			m, err := bodyMessage(res.Proto+" "+res.Status, responseHeaders(res), res.Header.Get("Content-Type"), res.Header.Get("Content-Encoding"), res.Body)
			if err != nil {
				s.parseError(err)
				return
			}
			s.f.log("response body id=%d stream=%d bytes=%d stored=%d truncated=%v",
				x.exchange.ID, s.id, m.Bytes, len(m.RawBody), m.Truncated)
			x.mu.Lock()
			x.exchange.Response = &m
			x.exchange.Status = res.StatusCode
			end := r.timestamp(r.read - int64(br.Buffered()) - 1)
			if end.After(x.exchange.Started) {
				x.exchange.Duration = end.Sub(x.exchange.Started)
			}
			if x.complete {
				s.f.publish(x.snapshot())
			}
			x.mu.Unlock()
			if res.StatusCode == 101 || x.req.Method == http.MethodConnect && res.StatusCode >= 200 && res.StatusCode < 300 {
				s.abort()
				return
			}
			break
		}
	}
}
