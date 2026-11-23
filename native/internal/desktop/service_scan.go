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
	a.scanLoading.progressHint = "loading.web_scan_progress_hint"
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
				a.update(func() {
					a.scanStatus = i18npkg.MessageText("service_scan.probing_http_services_d_d", done, total)
					a.setScanProgress(done, total)
				})
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

func (a *app) setScanProgress(done, total int) {
	d := a.scanLoading
	if d == nil || !d.active {
		return
	}
	d.completed, d.total = done, total
	if d.window != nil {
		d.window.Invalidate()
	}
}

// serviceScanControls renders the right side of the adapter/port row.
func (a *app) serviceScanControls(c *ui.Context) {
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
	placeholder := a.tr("service_scan.select_a_service")
	opts := []string{placeholder}
	for _, s := range a.services {
		opts = append(opts, a.serviceLabel(s))
	}
	ui.Text(c, a.tr("service_scan.discovered_services")).TextColor(c.Theme().TextMuted)
	choice := placeholder
	for _, service := range a.services {
		if service.Key() == a.serviceChoice {
			choice = a.serviceLabel(service)
			break
		}
	}
	if ui.Select(c.Key("http-services"), &choice, opts).Width(170).Label(a.tr("service_scan.discovered_http_services")).Disabled(a.running || a.scanning || len(a.services) == 0 || a.pcap != "").Changed() {
		if choice == placeholder {
			a.serviceChoice = ""
		} else {
			for _, service := range a.services {
				if a.serviceLabel(service) == choice {
					a.chooseService(service.Key())
					break
				}
			}
		}
	}
	if _, knownJVM := a.selectedJVMService(); knownJVM {
		if ui.Button(c, a.tr("service_scan.jvm_routes_and_tracing")).Disabled(a.scanning || a.pcap != "").Clicked() {
			a.openArthas()
		}
	}
}

func (a *app) serviceLabel(service discoverypkg.HTTPService) string {
	return a.tr(service.Label())
}
