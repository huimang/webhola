package replay

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	capturepkg "webhola/native/internal/capture"
	i18npkg "webhola/native/internal/i18n"
)

// Register the fresh connection before its request is sent. Passive capture
// can then omit that connection: the replay worker supplies the same exchange.
type ReplayEndpoints struct {
	mu    sync.Mutex
	items map[string]time.Time
}

func normalizeEndpoint(s string) string {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return s
	}
	host := strings.Trim(s[:i], "[]")
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	return net.JoinHostPort(host, s[i+1:])
}

func replayKey(client, server string) string {
	return normalizeEndpoint(client) + "|" + normalizeEndpoint(server)
}

func (r *ReplayEndpoints) Add(client, server string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.items == nil {
		r.items = map[string]time.Time{}
	}
	now := time.Now()
	for k, until := range r.items {
		if now.After(until) {
			delete(r.items, k)
		}
	}
	r.items[replayKey(client, server)] = now.Add(2 * time.Minute)
}

func (r *ReplayEndpoints) Contains(client, server string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := replayKey(client, server)
	until, ok := r.items[key]
	if ok && time.Now().After(until) {
		delete(r.items, key)
		return false
	}
	return ok
}

func ReplayRequest(e capturepkg.Exchange) (*http.Request, error) {
	if e.Request.Truncated {
		return nil, i18npkg.MessageError("replay.the_request_body_is_truncated_incomplete_data")
	}
	if strings.EqualFold(e.Method, "CONNECT") {
		return nil, i18npkg.MessageError("replay.resending_connect_tunnel_requests_is_not_supported")
	}
	h, err := textproto.NewReader(bufio.NewReader(strings.NewReader(strings.TrimRight(e.Request.Headers, "\r\n") + "\r\n\r\n"))).ReadMIMEHeader()
	if err != nil {
		return nil, i18npkg.MessageError("replay.unable_to_parse_original_request_headers_w", err)
	}
	if encoding := h.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return nil, i18npkg.MessageError("replay.resending_compressed_or_encoded_request_bodies_is")
	}
	body := e.Request.RawBody
	if body == nil {
		body = []byte(e.Request.Body)
	}
	if declared := h.Get("Content-Length"); declared != "" {
		n, err := strconv.ParseInt(declared, 10, 64)
		if err != nil || n < 0 {
			return nil, i18npkg.MessageError("replay.the_original_content_length_is_invalid")
		}
		if n != int64(len(body)) {
			return nil, i18npkg.MessageError("replay.captured_body_does_not_match_the_original")
		}
	}
	if e.Request.Bytes > len(body) {
		return nil, i18npkg.MessageError("replay.the_request_body_has_not_been_fully")
	}
	req, err := http.NewRequest(e.Method, e.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if req.URL.Scheme != "http" || req.URL.Hostname() == "" || req.URL.User != nil {
		return nil, i18npkg.MessageError("replay.only_plain_http_requests_with_a_complete")
	}
	req.Header = http.Header(h)
	req.Host = req.Header.Get("Host")
	req.Header.Del("Host")
	for _, key := range strings.Split(req.Header.Get("Connection"), ",") {
		if strings.TrimSpace(key) != "" {
			req.Header.Del(strings.TrimSpace(key))
		}
	}
	for _, key := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "TE", "Trailer", "Transfer-Encoding", "Upgrade", "Content-Length", "Expect"} {
		req.Header.Del(key)
	}
	// Transport calculates framing for the captured entity body on a fresh connection.
	req.Close = true
	if _, ok := req.Header["User-Agent"]; !ok {
		req.Header["User-Agent"] = []string{""}
	}
	return req, nil
}

func ResponseMessage(res *http.Response) (capturepkg.Message, error) {
	m := capturepkg.Message{Line: res.Proto + " " + res.Status, ContentType: res.Header.Get("Content-Type")}
	var headers bytes.Buffer
	_ = res.Header.Write(&headers)
	m.Headers = headers.String()
	var reader io.Reader = res.Body
	if res.ContentLength != 0 && (res.Request == nil || res.Request.Method != http.MethodHead) && strings.EqualFold(strings.TrimSpace(res.Header.Get("Content-Encoding")), "gzip") {
		gz, err := gzip.NewReader(res.Body)
		if err != nil {
			return m, err
		}
		defer gz.Close()
		reader = gz
	}
	b, err := io.ReadAll(io.LimitReader(reader, capturepkg.MaxBody+1))
	if err != nil {
		return m, err
	}
	m.Bytes = len(b)
	if len(b) > capturepkg.MaxBody {
		m.Truncated = true
		b = b[:capturepkg.MaxBody]
	}
	m.RawBody = b
	if utf8.Valid(b) && !bytes.ContainsRune(b, 0) {
		m.Body = string(b)
	} else {
		m.Body = hex.Dump(b)
	}
	return m, nil
}

func RunReplay(ctx context.Context, req *http.Request, original capturepkg.Exchange, register func(string, string)) (capturepkg.Exchange, error) {
	result := original
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var mu sync.Mutex
	clientAddr, serverAddr := "", ""
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: true, DisableCompression: true, MaxResponseHeaderBytes: 1 << 20, ResponseHeaderTimeout: 20 * time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			c, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, address)
			if err == nil {
				local, remote := c.LocalAddr().String(), c.RemoteAddr().String()
				register(local, remote)
				mu.Lock()
				clientAddr, serverAddr = local, remote
				mu.Unlock()
			}
			return c, err
		}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req.WithContext(ctx))
	mu.Lock()
	result.Client, result.Server = clientAddr, serverAddr
	mu.Unlock()
	if err != nil {
		return result, err
	}
	defer res.Body.Close()
	result.Status = res.StatusCode
	m, err := ResponseMessage(res)
	result.Response = &m
	result.Duration = time.Since(result.Started)
	return result, err
}
