package desktop

import (
	"context"
	"time"

	"github.com/egoist/mygo/ui"

	discoverypkg "webhola/native/internal/discovery"
	i18npkg "webhola/native/internal/i18n"
)

func (a *app) scanServices() {
	if a.scanning || a.running || a.pcap != "" {
		return
	}
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	previousService, previousPort := a.serviceChoice, a.port
	a.scanCancel = cancel
	a.scanning = true
	a.scanStatus = i18npkg.MessageText("service_scan.reading_local_tcp_listeners")
	a.services = nil
	a.serviceChoice = ""
	a.scanLoading = a.openLoading(a.win, "loading.web_scan_title", "loading.web_scan_hint", func() string { return a.scanStatus }, cancel)
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		defer cancel()
		list, err := discoverypkg.ListListeners(ctx)
		adapters, _ := discoverypkg.LocalAdapters()
		var found []discoverypkg.HTTPService
		if err == nil {
			found = discoverypkg.ScanListeners(ctx, list, func(ctx context.Context, l discoverypkg.Listener) (discoverypkg.HTTPService, bool) {
				s, ok := discoverypkg.ProbeHTTP(ctx, l)
				if !ok && l.JVM {
					return discoverypkg.HTTPService{Listener: l}, true
				}
				return s, ok
			}, func(done, total int) {
				a.update(func() { a.scanStatus = i18npkg.MessageText("service_scan.probing_http_services_d_d", done, total) })
			})
		}
		canceled := ctx.Err()
		a.update(func() {
			a.scanning = false
			a.scanCancel = nil
			a.scanLoading.close()
			a.scanLoading = nil
			a.services = found
			a.serviceAdapters = adapters
			if a.port == previousPort && previousService != "" {
				for _, process := range found {
					if process.Key() == previousService {
						a.serviceChoice = previousService
						a.chooseService(previousService)
						break
					}
				}
			}
			if err != nil {
				a.scanStatus = i18npkg.MessageText("service_scan.scan_failed") + i18npkg.ErrorMessage(err)
				return
			}
			if canceled != nil {
				a.scanStatus = i18npkg.MessageText("service_scan.scan_finished_found_d_http_jvm_services", len(found))
				return
			}
			if len(found) == 0 {
				a.scanStatus = i18npkg.MessageText("service_scan.no_http_services_confirmed_enter_a_port")
			} else {
				a.scanStatus = i18npkg.MessageText("service_scan.found_d_http_jvm_services_select_one", len(found))
			}
		})
	}()
}

func (a *app) serviceScanView(c *ui.Context) {
	ui.Row(c).FillWidth().Gap(10).Children(func() {
		label := i18npkg.MessageText("service_scan.scan_http_services")
		if a.scanning {
			label = i18npkg.MessageText("service_scan.stop_scan")
		}
		if ui.Button(c, a.tr(label)).Disabled(a.running || a.pcap != "").Clicked() {
			if a.scanning {
				a.scanCancel()
			} else {
				a.scanServices()
			}
		}
		var opts []string
		for _, s := range a.services {
			opts = append(opts, a.serviceLabel(s))
		}
		ui.Text(c, a.tr("service_scan.discovered_services")).TextColor(c.Theme().TextMuted)
		choice := ""
		for _, service := range a.services {
			if service.Key() == a.serviceChoice {
				choice = a.serviceLabel(service)
				break
			}
		}
		if ui.Select(c.Key("http-services"), &choice, opts).Grow(1).Label(a.tr("service_scan.discovered_http_services")).Disabled(a.running || a.scanning || len(opts) == 0 || a.pcap != "").Changed() {
			for _, service := range a.services {
				if a.serviceLabel(service) == choice {
					a.chooseService(service.Key())
					break
				}
			}
		}
		if _, knownJVM := a.selectedJVMService(); knownJVM {
			if ui.Button(c, a.tr("service_scan.jvm_routes_and_tracing")).Disabled(a.scanning || a.pcap != "").Clicked() {
				a.openArthas()
			}
		}
	})
	if a.scanStatus != "" {
		ui.Text(c, a.tr(a.scanStatus)).FontSize(12).TextColor(c.Theme().TextMuted).Role(ui.RoleStatus)
	} else {
		ui.Text(c, a.tr("service_scan.click_scan_to_send_head_probes_to")).FontSize(12).TextColor(c.Theme().TextMuted)
	}
}

func (a *app) serviceLabel(service discoverypkg.HTTPService) string {
	return a.tr(service.Label())
}
