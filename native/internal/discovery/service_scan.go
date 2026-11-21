package discovery

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	capturepkg "webhola/native/internal/capture"
	i18npkg "webhola/native/internal/i18n"
)

type Listener struct {
	IP      string
	Port    int
	PID     uint32
	Process string
	JVM     bool
}

type HTTPService struct {
	Listener
	Status    int
	Endpoints []HTTPService
}

func (s HTTPService) Key() string {
	return fmt.Sprintf("port:%d", s.Port)
}

func endpointKey(l Listener) string {
	return fmt.Sprintf("%d|%s", l.PID, net.JoinHostPort(l.IP, strconv.Itoa(l.Port)))
}

func (s HTTPService) EndpointList() []HTTPService {
	if len(s.Endpoints) > 0 {
		return s.Endpoints
	}
	return []HTTPService{s}
}

func (s HTTPService) kind() string {
	kind := "HTTP"
	if s.Status == 0 {
		kind = i18npkg.MessageText("service_scan.http_unconfirmed")
	}
	if s.JVM {
		kind += " / JVM"
	}
	return kind
}

func (s HTTPService) Label() string {
	process := s.Process
	if process == "" {
		process = i18npkg.MessageText("service_scan.unknown_process")
	}
	return fmt.Sprintf("%d · %s · %s · PID %d", s.Port, s.kind(), process, s.PID)
}

type LocalAdapter struct {
	Name, CaptureName string
	Addresses         []net.IP
}

func ProbeHTTP(ctx context.Context, l Listener) (HTTPService, bool) {
	ip := net.ParseIP(strings.Split(l.IP, "%")[0])
	if ip == nil || l.Port < 1 || l.Port > 65535 {
		return HTTPService{}, false
	}
	host := l.IP
	if ip.IsUnspecified() {
		if ip.To4() != nil {
			host = "127.0.0.1"
		} else {
			host = "::1"
		}
	}
	address := net.JoinHostPort(host, strconv.Itoa(l.Port))
	ctx, cancel := context.WithTimeout(ctx, 900*time.Millisecond)
	defer cancel()
	tr := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 500 * time.Millisecond}).DialContext, DisableKeepAlives: true, ResponseHeaderTimeout: 800 * time.Millisecond, MaxResponseHeaderBytes: 16 * 1024}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, (&url.URL{Scheme: "http", Host: address, Path: "/"}).String(), nil)
	if err != nil {
		return HTTPService{}, false
	}
	req.Header.Set("User-Agent", "Webhola/0.2.0 HTTP-Service-Scan")
	res, err := client.Do(req)
	if err != nil {
		return HTTPService{}, false
	}
	res.Body.Close()
	if res.ProtoMajor != 1 {
		return HTTPService{}, false
	}
	return HTTPService{Listener: l, Status: res.StatusCode}, true
}

func ScanListeners(ctx context.Context, list []Listener, probe func(context.Context, Listener) (HTTPService, bool), progress func(int, int)) []HTTPService {
	// Probe every distinct endpoint before grouping by port.
	unique := make([]Listener, 0, len(list))
	seen := map[string]bool{}
	for _, l := range list {
		key := endpointKey(l)
		if !seen[key] {
			seen[key] = true
			unique = append(unique, l)
		}
	}
	var mu sync.Mutex
	var found []HTTPService
	done := 0
	jobs := make(chan Listener)
	var workers sync.WaitGroup
	for i := 0; i < min(8, len(unique)); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for l := range jobs {
				if ctx.Err() != nil {
					continue
				}
				s, ok := probe(ctx, l)
				mu.Lock()
				if ok {
					s.Listener = l
					found = append(found, s)
				}
				done++
				if progress != nil {
					progress(done, len(unique))
				}
				mu.Unlock()
			}
		}()
	}
send:
	for _, l := range unique {
		select {
		case jobs <- l:
		case <-ctx.Done():
			break send
		}
	}
	close(jobs)
	workers.Wait()
	return groupPortServices(unique, found)
}

// One row per port, retaining all its bindings internally.
func groupPortServices(list []Listener, found []HTTPService) []HTTPService {
	results := map[string]HTTPService{}
	visible := map[string]bool{}
	for _, s := range found {
		results[endpointKey(s.Listener)] = s
		visible[s.Key()] = true
	}
	groups := map[string][]HTTPService{}
	seen := map[string]bool{}
	for _, l := range list {
		endpoint := endpointKey(l)
		if seen[endpoint] {
			continue
		}
		seen[endpoint] = true
		s, ok := results[endpoint]
		if !ok {
			s = HTTPService{Listener: l}
		}
		if !visible[s.Key()] {
			continue
		}
		s.Endpoints = nil
		groups[s.Key()] = append(groups[s.Key()], s)
	}
	grouped := make([]HTTPService, 0, len(groups))
	for _, endpoints := range groups {
		sort.Slice(endpoints, func(i, j int) bool { return preferredEndpoint(endpoints[i], endpoints[j]) })
		s := endpoints[0]
		s.Endpoints = endpoints

		grouped = append(grouped, s)
	}
	sort.Slice(grouped, func(i, j int) bool {
		if grouped[i].Port != grouped[j].Port {
			return grouped[i].Port < grouped[j].Port
		}
		if grouped[i].IP != grouped[j].IP {
			return grouped[i].IP < grouped[j].IP
		}
		return grouped[i].PID < grouped[j].PID
	})
	return grouped
}

func preferredEndpoint(a, b HTTPService) bool {
	if (a.Status != 0) != (b.Status != 0) {
		return a.Status != 0
	}
	if a.Port != b.Port {
		return a.Port < b.Port
	}
	if left, right := serviceAddressRank(a.IP), serviceAddressRank(b.IP); left != right {
		return left < right
	}
	if a.IP != b.IP {
		return a.IP < b.IP
	}
	return a.PID < b.PID
}

func serviceAddressRank(address string) int {
	ip := net.ParseIP(strings.Split(address, "%")[0])
	if ip == nil {
		return 6
	}
	rank := 2
	if ip.IsLoopback() {
		rank = 0
	} else if ip.IsUnspecified() {
		rank = 4
	}
	if ip.To4() == nil {
		rank++
	}
	return rank
}

func LoopbackCaptureDevice(devices []capturepkg.Device) string {
	var label string
	for _, d := range devices {
		name, _, _ := strings.Cut(strings.ToLower(d.Label), " (")
		if name == `\device\npf_loopback` || name == "lo" || name == "lo0" {
			if label != "" && label != d.Label {
				return ""
			}
			label = d.Label
		}
	}
	return label
}

func DefaultCaptureDevice(devices []capturepkg.Device, current string) string {
	for _, d := range devices {
		if d.Label == current {
			return current
		}
	}
	// An unselected adapter is safer than silently capturing the wrong network.
	return ""
}

func MatchServiceDevice(s HTTPService, devices []capturepkg.Device, adapters []LocalAdapter) (string, string) {
	ip := net.ParseIP(strings.Split(s.IP, "%")[0])
	if ip == nil {
		return "", i18npkg.MessageText("service_scan.unable_to_identify_the_listening_address_select")
	}
	if ip.IsUnspecified() {
		return "", i18npkg.MessageText("service_scan.the_service_listens_on_all_addresses_select")
	}
	candidates := map[string]bool{}
	if ip.IsLoopback() {
		for _, d := range devices {
			name, _, _ := strings.Cut(strings.ToLower(d.Label), " (")
			if name == `\device\npf_loopback` || name == "lo" || name == "lo0" {
				candidates[d.Label] = true
			}
		}
	} else {
		for _, a := range adapters {
			hasIP := false
			for _, addr := range a.Addresses {
				if ip.Equal(addr) {
					hasIP = true
					break
				}
			}
			if !hasIP {
				continue
			}
			for _, d := range devices {
				name, _, _ := strings.Cut(d.Label, " (")
				if (a.CaptureName != "" && strings.EqualFold(name, a.CaptureName)) || strings.EqualFold(name, a.Name) || (a.Name != "" && strings.EqualFold(d.Label, name+" ("+a.Name+")")) {
					candidates[d.Label] = true
				}
			}
		}
	}
	if len(candidates) == 1 {
		for label := range candidates {
			return label, i18npkg.MessageText("service_scan.adapter_selected_automatically_from_the_listening_address")
		}
	}
	return "", i18npkg.MessageText("service_scan.could_not_uniquely_match_a_capture_adapter")
}
