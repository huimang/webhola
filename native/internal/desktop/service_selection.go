package desktop

import (
	"net"
	"strconv"
	"strings"

	discoverypkg "webhola/native/internal/discovery"
	i18npkg "webhola/native/internal/i18n"
)

func (a *app) selectedServiceEndpoint() (discoverypkg.HTTPService, bool) {
	for _, s := range a.services {
		if s.Key() == a.serviceChoice && strconv.Itoa(s.Port) == strings.TrimSpace(a.port) {
			return s, true
		}
	}
	return discoverypkg.HTTPService{}, false
}

func (a *app) selectedJVMService() (discoverypkg.Listener, bool) {
	s, ok := a.selectedServiceEndpoint()
	if !ok || !s.JVM || s.PID == 0 {
		return discoverypkg.Listener{}, false
	}
	// Do not attach when different processes own bindings on the same port.
	for _, endpoint := range s.EndpointList() {
		if endpoint.PID != s.PID {
			return discoverypkg.Listener{}, false
		}
	}
	return s.Listener, true
}

func (a *app) chooseService(key string) {
	if a.running || a.scanning {
		return
	}
	for _, s := range a.services {
		if s.Key() == key {
			a.serviceChoice = key
			a.port = strconv.Itoa(s.Port)
			a.applyServiceEndpoint()
			return
		}
	}
}

func (a *app) applyServiceEndpoint() {
	s, ok := a.selectedServiceEndpoint()
	if !ok {
		return
	}
	manual := false
	if a.ifaceManual {
		for _, d := range a.deviceList {
			if d.Label == a.iface {
				manual = true
				break
			}
		}
	}
	var note string
	if manual {
		note = i18npkg.MessageText("service_scan.manual_adapter_preserved")
	} else {
		a.ifaceManual = false
		// A representative loopback binding must not hide the other networks.
		ambiguous := false
		for _, endpoint := range s.EndpointList() {
			if endpoint.IP != s.IP {
				ambiguous = true
				break
			}
		}
		if ambiguous {
			note = i18npkg.MessageText("service_scan.multiple_bindings_select_adapter")
		} else {
			iface, matched := discoverypkg.MatchServiceDevice(s, a.deviceList, a.serviceAdapters)
			note = matched
			if iface != "" {
				a.iface = iface
			}
		}
	}
	if s.Status == 0 {
		note = i18npkg.MessageText("service_scan.port_not_confirmed_http") + note
	}
	a.scanStatus = i18npkg.MessageText("service_scan.endpoint_selected", net.JoinHostPort(s.IP, a.port), note)
}
