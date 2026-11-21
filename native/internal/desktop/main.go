package desktop

import (
	"context"
	_ "embed"
	"flag"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	capturepkg "webhola/native/internal/capture"
	discoverypkg "webhola/native/internal/discovery"
	i18npkg "webhola/native/internal/i18n"
	jvmpkg "webhola/native/internal/jvm"
	replaypkg "webhola/native/internal/replay"
)

//go:embed resources/icon.svg
var iconBytes []byte

var appIcon = ui.MustParseSVG(iconBytes)

//go:embed resources/icon.png
var windowIcon []byte

type app struct {
	language                                  string
	themeMode                                 string
	arthasConfig                              jvmpkg.ArthasConfig
	arthasDraft                               jvmpkg.ArthasConfig
	arthas                                    *arthasState
	workspace                                 int
	arthasCleanup                             chan struct{}
	replaying                                 bool
	replayFrom                                uint64
	nextEntry                                 uint64
	captureIDs                                map[uint64]uint64
	captureFloor                              uint64
	replayEndpoints                           replaypkg.ReplayEndpoints
	aboutWindow                               *mygo.Window
	bodyPreview                               *bodyPreview
	bodyWindow                                *mygo.Window
	bodyFullscreen                            *bodyPreview
	javaSource                                *javaSourceViewer
	javaSources                               []*javaSourceViewer
	scanning                                  bool
	scanCancel                                context.CancelFunc
	scanLoading                               *loadingDialog
	services                                  []discoverypkg.HTTPService
	serviceChoice, scanStatus                 string
	ifaceManual                               bool
	serviceAdapters                           []discoverypkg.LocalAdapter
	settingsWindow                            *mygo.Window
	dispatch                                  func(func())
	tsharkPath, settingsDraft, settingsStatus string
	showSettings, settingsBusy                bool
	win                                       *mygo.Window
	ctx                                       context.Context
	workers                                   sync.WaitGroup
	cancel                                    context.CancelFunc
	events                                    chan capturepkg.Exchange
	done                                      chan error
	entries                                   []capturepkg.Exchange
	selected                                  uint64
	discarded                                 uint64
	floor                                     uint64
	port, iface, search, tab, status, pcap    string
	deviceList                                []capturepkg.Device
	running, refreshing, failed, stopping     bool
}

func newApp(ctx context.Context) *app {
	return &app{ctx: ctx, port: "8080", tab: "http.request", status: i18npkg.MessageText("main.select_an_adapter_and_http_port_to")}
}

func (a *app) update(fn func()) {
	if a.dispatch != nil {
		a.dispatch(fn)
		return
	}
	if a.ctx.Err() == nil {
		a.win.Update(func() {
			if a.ctx.Err() == nil {
				fn()
			}
		})
	}
}

func (a *app) refresh() {
	if a.refreshing || a.running {
		return
	}
	a.refreshing = true
	a.status = i18npkg.MessageText("main.checking_tshark_and_available_adapters")
	a.failed = false
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		path, err := capturepkg.ConfiguredTShark(a.tsharkPath)
		var list []capturepkg.Device
		if err == nil {
			ctx, cancel := context.WithTimeout(a.ctx, 8*time.Second)
			defer cancel()
			list, err = capturepkg.Devices(ctx, path)
		}
		a.update(func() {
			a.refreshing = false
			if err != nil {
				a.deviceList = nil
				a.iface = ""
				a.status = i18npkg.ErrorMessage(err)
				a.failed = true
				return
			}
			a.deviceList = list
			oldInterface := a.iface
			a.iface = discoverypkg.DefaultCaptureDevice(list, a.iface)
			if oldInterface != a.iface {
				a.ifaceManual = false
			}
			if a.serviceChoice != "" {
				a.chooseService(a.serviceChoice)
			}
			a.status = i18npkg.MessageText("main.adapters_found_select_the_loopback_adapter_for")
		})
	}()
}

func (a *app) start() {
	if a.running || a.refreshing || a.scanning || a.replaying {
		return
	}
	path, err := capturepkg.ConfiguredTShark(a.tsharkPath)
	if err != nil {
		a.status = i18npkg.ErrorMessage(err)
		a.failed = true
		return
	}
	id := ""
	for _, d := range a.deviceList {
		if d.Label == a.iface {
			id = d.ID
		}
	}
	args, err := capturepkg.CaptureArgs(id, a.port, a.pcap)
	if err != nil {
		a.status = i18npkg.ErrorMessage(err)
		a.failed = true
		return
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.cancel = cancel
	a.running = true
	a.stopping = false
	a.failed = false
	a.status = i18npkg.MessageText("main.starting_capture")
	a.entries = nil
	a.selected = 0
	a.discarded = 0
	a.floor = 0
	a.captureIDs = nil
	a.captureFloor = 0
	a.events = make(chan capturepkg.Exchange, 128)
	a.done = make(chan error, 1)
	events, done := a.events, a.done
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		defer cancel()
		err := capturepkg.Capture(ctx, path, args, func() {
			a.update(func() {
				if !a.stopping {
					a.status = i18npkg.MessageText("main.capture_started_waiting_for_http_traffic")
				}
			})
		}, func(e capturepkg.Exchange) {
			select {
			case events <- e:
			case <-ctx.Done():
			}
		})
		close(events)
		done <- err
	}()
}

func (a *app) accept(e capturepkg.Exchange) {
	if e.ID > a.nextEntry {
		a.nextEntry = e.ID
	}
	if e.ID <= a.floor {
		return
	}
	for i := len(a.entries) - 1; i >= 0; i-- {
		if a.entries[i].ID == e.ID {
			a.entries[i] = e
			return
		}
	}
	if len(a.entries) == capturepkg.MaxEntries {
		if e.ID < a.entries[0].ID {
			return
		}
		a.floor = a.entries[0].ID
		a.entries = append([]capturepkg.Exchange{}, a.entries[1:]...)
		a.discarded++
	}
	a.entries = append(a.entries, e)
	if a.selected == 0 || (len(a.entries) > 0 && a.selected < a.entries[0].ID) {
		a.selected = e.ID
	}
}

func (a *app) drain() {
	if !a.running {
		return
	}
	for i := 0; i < 256; i++ {
		select {
		case e, ok := <-a.events:
			if !ok {
				a.events = nil
				continue
			}
			a.acceptCaptured(e)
		default:
			i = 256
		}
	}
	if a.events != nil {
		return
	}
	select {
	case err := <-a.done:
		a.running = false
		a.cancel = nil
		a.stopping = false
		if err != nil {
			a.failed = true
			a.status = i18npkg.ErrorMessage(err)
		} else {
			a.status = i18npkg.MessageText("main.capture_ended_current_records_have_been_retained")
		}
	default:
	}
}

func (a *app) clear() {
	a.bodyPreview = nil
	for _, e := range a.entries {
		if e.ID > a.floor {
			a.floor = e.ID
		}
	}
	a.entries = nil
	a.selected = 0
	a.discarded = 0
}

func (a *app) matches(e capturepkg.Exchange) bool {
	q := strings.ToLower(strings.TrimSpace(a.search))
	return q == "" || strings.Contains(strings.ToLower(fmt.Sprintf("%s %s %d %s %s", e.Method, e.URL, e.Status, e.Client, e.Server)), q)
}

func responseLabel(e capturepkg.Exchange) string {
	if e.Error != "" {
		return i18npkg.MessageText("main.send_failed")
	}
	if e.Response == nil {
		return i18npkg.MessageText("main.waiting_for_response")
	}
	return fmt.Sprintf("%d · %.1f ms · %d B", e.Status, float64(e.Duration)/float64(time.Millisecond), e.Response.Bytes)
}

func (a *app) view(c *ui.Context) {
	a.applyTheme(c)
	if a.running {
		a.drain()
		if a.running {
			c.After(100 * time.Millisecond)
		}
	}
	ui.Column(c).FillWidth().FillHeight().Gap(0).Children(func() {
		ui.Row(c).FillWidth().Padding(8, 16).Gap(8).Background(c.Theme().Surface).Children(func() {
			labels := []string{a.tr("workspace.http")}
			if a.arthas != nil {
				labels = append(labels, a.tr("workspace.jvm"))
			}
			for _, source := range a.javaSources {
				labels = append(labels, source.tabLabel())
			}
			ui.Tabs(c.Key("workspace-tabs"), &a.workspace, labels...).Label(a.tr("workspace.tabs"))
			if source := a.selectedJavaSource(); source != nil {
				if ui.Button(c.Key("workspace-close-source"), "×").Label(a.tr("source.close_tab")).Clicked() {
					a.closeJavaSource(source)
				}
			} else if a.arthas != nil && a.workspace == 1 {
				if ui.Button(c.Key("workspace-close-jvm"), "×").Label(a.tr("workspace.close_jvm")).Clicked() {
					a.closeArthas()
				}
			}
		})
		ui.Box(c).FillWidth().Height(1).Background(c.Theme().Border)
		ui.Column(c.Key("workspace-content")).FillWidth().Grow(1).Children(func() {
			if source := a.selectedJavaSource(); source != nil {
				a.javaSource = source
				a.javaSourceView(c, source)
			} else if a.workspace == 1 && a.arthas != nil {
				a.arthasView(c)
			} else {
				a.httpView(c)
			}
		})
	})
}

func (a *app) httpView(c *ui.Context) {
	ui.Column(c).FillWidth().FillHeight().Gap(0).Children(func() {
		ui.Column(c).FillWidth().Padding(16, 24).Gap(10).Children(func() {
			a.serviceScanView(c)
			ui.Row(c).Gap(10).FillWidth().Children(func() {
				ui.Text(c, a.tr("main.network_adapter")).Bold()
				var opts []string
				for _, d := range a.deviceList {
					opts = append(opts, d.Label)
				}
				if ui.Select(c.Key("interface"), &a.iface, opts).Grow(1).MinWidth(220).Label(a.tr("main.capture_adapter")).Disabled(a.running || a.refreshing || a.pcap != "").Changed() {
					a.ifaceManual = true
					if a.serviceChoice != "" {
						a.applyServiceEndpoint()
					}
				}
				if ui.Button(c, a.tr("main.refresh_adapters")).Disabled(a.running || a.refreshing || a.pcap != "").Clicked() {
					a.refresh()
				}
				ui.Text(c, a.tr("main.port")).Bold()
				if ui.TextInput(c.Key("port"), &a.port).Width(85).Label(a.tr("main.http_port")).Disabled(a.running).Changed() {
					a.serviceChoice = ""
				}
				label := i18npkg.MessageText("main.start_capture")
				if a.pcap != "" {
					label = i18npkg.MessageText("main.read_file")
				}
				if a.running {
					label = i18npkg.MessageText("main.stop_capture")
				}
				if a.stopping {
					label = i18npkg.MessageText("main.stopping")
				}
				if ui.PrimaryButton(c, a.tr(label)).Disabled(a.refreshing || a.stopping || a.scanning || (!a.running && a.replaying)).Clicked() {
					if a.running {
						a.stopping = true
						a.cancel()
						a.status = i18npkg.MessageText("main.stopping_capture")
					} else {
						a.start()
					}
				}
			})
			color := c.Theme().TextMuted
			if a.failed {
				color = c.Theme().Danger
			} else if a.running {
				color = c.Theme().Accent
			}
			ui.Text(c, a.tr(a.status)).FontSize(13).TextColor(color).Role(ui.RoleStatus)
		})
		ui.Box(c).FillWidth().Height(1).Background(c.Theme().Border)
		ui.Row(c).FillWidth().Grow(1).Gap(1).Background(c.Theme().Border).Children(func() {
			ui.Column(c).Width(390).FillHeight().Padding(16).Gap(12).Background(c.Theme().Surface).Children(func() {
				ui.Row(c).FillWidth().Gap(10).Children(func() {
					ui.Text(c, a.tr("main.requests")).FontSize(17).Bold()
					ui.Box(c).Grow(1)
					ui.Textf(c, a.tr("main.record_count"), a.visibleCount(), len(a.entries)).TextColor(c.Theme().TextMuted)
					if ui.Button(c, a.tr("main.clear")).Clicked() {
						a.clear()
					}
				})
				ui.TextInput(c.Key("search"), &a.search).FillWidth().Placeholder(a.tr("main.search_url_method_status_code_or_ip")).Label(a.tr("main.search_requests"))
				ui.Scroll(c.Key("requests")).FillWidth().Grow(1).Children(func() {
					ui.Column(c).FillWidth().Gap(6).Children(func() {
						if len(a.entries) == 0 {
							ui.Text(c, a.tr("main.no_requests_captured_yet")).Bold()
							ui.Text(c, a.tr("main.start_capture_then_send_requests_to_the")).TextColor(c.Theme().TextMuted)
						} else if a.visibleCount() == 0 {
							ui.Text(c, a.tr("main.no_matching_requests")).TextColor(c.Theme().TextMuted)
						}
						for i := len(a.entries) - 1; i >= 0; i-- {
							e := a.entries[i]
							if !a.matches(e) {
								continue
							}
							bg := c.Theme().Surface
							if e.ID == a.selected {
								bg = c.Theme().Selection
							}
							row := ui.Column(c.Key(fmt.Sprintf("request-%d", e.ID))).FillWidth().Padding(12).Gap(4).Radius(8).Background(bg).Border(1, c.Theme().Border).OnClick(func() { a.selected = e.ID }).Label(fmt.Sprintf("%s %s", e.Method, e.URL))
							row.Children(func() {
								ui.Row(c).FillWidth().Gap(8).Children(func() {
									ui.Text(c, e.Method).Bold().TextColor(c.Theme().Accent)
									if e.Replayed {
										ui.Text(c, a.tr("main.resent")).FontSize(11).TextColor(c.Theme().TextMuted)
									}
									ui.Box(c).Grow(1)
									ui.Text(c, e.Started.Local().Format("15:04:05.000")).FontSize(11).TextColor(c.Theme().TextMuted)
								})
								ui.Text(c, e.URL).FontSize(13)
								col := c.Theme().TextMuted
								if e.Status >= 400 {
									col = c.Theme().Warning
								}
								if e.Status >= 500 {
									col = c.Theme().Danger
								}
								if e.Error != "" {
									col = c.Theme().Danger
								}
								ui.Row(c).FillWidth().Gap(8).Children(func() {
									ui.Text(c, a.tr(responseLabel(e))).FontSize(12).TextColor(col)
									ui.Box(c).Grow(1)
									label := i18npkg.MessageText("main.resend")
									if a.replaying && a.replayFrom == e.ID {
										label = i18npkg.MessageText("main.sending")
									}
									if ui.Button(c.Key(fmt.Sprintf("replay-%d", e.ID)), a.tr(label)).Disabled(a.replaying).Clicked() {
										a.resend(e)
									}
								})
							})
						}
					})
				})
			})
			ui.Column(c).Grow(1).FillHeight().Padding(20).Gap(12).Background(c.Theme().Surface).Children(func() { a.details(c) })
		})
		ui.Row(c).FillWidth().Padding(10, 24).Gap(20).Children(func() {
			ui.Text(c, a.tr("main.plain_http_1_x_processed_locally")).FontSize(12).TextColor(c.Theme().TextMuted)
			ui.Box(c).Grow(1)
			ui.Textf(c, a.tr("main.up_to_d_records_d_discarded"), capturepkg.MaxEntries, a.discarded).FontSize(12).TextColor(c.Theme().TextMuted)
		})
	})
}

func (a *app) visibleCount() int {
	n := 0
	for _, e := range a.entries {
		if a.matches(e) {
			n++
		}
	}
	return n
}

func (a *app) details(c *ui.Context) {
	var e *capturepkg.Exchange
	for i := range a.entries {
		if a.entries[i].ID == a.selected {
			e = &a.entries[i]
			break
		}
	}
	if e == nil {
		ui.Text(c, a.tr("main.inspect_an_http_exchange")).FontSize(22).Bold()
		ui.Text(c, a.tr("main.select_a_request_on_the_left_to")).TextColor(c.Theme().TextMuted)
		ui.Box(c).Height(16)
		ui.Text(c, a.tr("main.capture_traffic_on_existing_ports_without_changing")).TextColor(c.Theme().TextMuted)
		ui.Text(c, a.tr("main.only_captured_plain_http_traffic_is_shown")).TextColor(c.Theme().TextMuted)
		return
	}
	ui.Text(c, e.Method+"  "+e.URL).FontSize(18).Bold().Selectable()
	connection := e.Client + "  →  " + e.Server + "   ·   TCP #" + e.Stream
	if e.Replayed {
		connection = a.tr("main.resend")
		if e.Client != "" {
			connection += " · " + e.Client + " → " + e.Server
		}
	}
	ui.Text(c, connection).FontSize(12).TextColor(c.Theme().TextMuted).Selectable()
	ui.Row(c).Gap(8).Children(func() {
		for _, tab := range []string{"http.request", "http.response"} {
			if ui.Button(c, a.tr(tab)).Selected(a.tab == tab).Clicked() {
				a.tab = tab
			}
		}
		ui.Box(c).Grow(1)
		ui.Text(c, a.tr(responseLabel(*e))).FontSize(13).TextColor(c.Theme().TextMuted)
	})
	m := &e.Request
	if a.tab == "http.response" {
		m = e.Response
		if m == nil {
			if e.Error != "" {
				ui.Text(c, a.tr("main.resend_failed")).Bold()
				ui.Text(c, a.tr(e.Error)).TextColor(c.Theme().Danger)
				return
			}
			ui.Text(c, a.tr("main.no_final_response_captured_yet")).Bold()
			if len(e.Interim) > 0 {
				ui.Textf(c, a.tr("main.interim_responses_received_v"), e.Interim)
			}
			ui.Text(c, a.tr("main.the_request_may_still_be_processing_or")).TextColor(c.Theme().TextMuted)
			return
		}
	}
	if a.tab == "http.response" && len(e.Interim) > 0 {
		ui.Textf(c, a.tr("main.interim_responses_v"), e.Interim).TextColor(c.Theme().TextMuted)
	}
	if e.Error != "" {
		ui.Text(c, a.tr(e.Error)).TextColor(c.Theme().Danger)
	}
	ui.Text(c, a.tr("main.start_line_and_headers")).Bold()
	headers := m.Line + "\r\n" + m.Headers
	ui.TextArea(c.Key(fmt.Sprintf("headers-%d-%s", e.ID, a.tab)), &headers).ReadOnly(true).FillWidth().Height(180).Font("Consolas").FontSize(13).Label(a.tr("main.http_start_line_and_headers"))
	ui.Row(c).FillWidth().Children(func() {
		ui.Text(c, a.tr("body_view.body")).Bold()
		ui.Box(c).Grow(1)
		ui.Textf(c, a.tr("body_view.content_type_and_size"), m.ContentType, m.Bytes).FontSize(12).TextColor(c.Theme().TextMuted)
	})
	a.bodyView(c, fmt.Sprintf("body-%d-%s", e.ID, a.tab), m)
}

func Run() {
	demo := flag.Bool("demo", false, "Load clearly labelled sample traffic without capturing")
	pcap := flag.String("read", "", "Read a pcap/pcapng file instead of a live interface")
	flag.Parse()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newApp(ctx)
	if path, err := settingsPath(); err == nil {
		if s, err := readSettings(path); err == nil {
			a.language = i18npkg.NormalizeLanguage(s.Language)
			a.themeMode = normalizeThemeMode(s.ThemeMode)
			a.tsharkPath = s.TSharkPath
			a.arthasConfig = s.ArthasConfig
		} else {
			log.Printf("load settings: %v", err)
		}
	}
	a.pcap = *pcap
	if *demo {
		a.loadDemo()
	}
	mygo.Theme.SetSource(mygo.ThemeSource(normalizeThemeMode(a.themeMode)))
	mygo.App.WhenReady(func() {
		a.win = mygo.NewWindow(mygo.WindowOptions{Title: "Webhola", Width: 1200, Height: 820, MinWidth: 1100, MinHeight: 720, Content: ui.View(a.view)})
		if err := a.win.SetIcon(windowIcon); err != nil {
			log.Printf("window icon: %v", err)
		}
		a.win.Show()
		a.installMenu()
		a.win.OnClosed(func() {
			cancel()
			if a.cancel != nil {
				a.cancel()
			}
			mygo.App.Quit()
		})
		if a.pcap != "" {
			a.status = i18npkg.MessageText("main.offline_file") + a.pcap + i18npkg.MessageText("main.enter_an_http_port_then_click_read")
		} else if !*demo {
			a.refresh()
		}
	})
	err := mygo.App.Run()
	cancel()
	a.workers.Wait()
	if err != nil {
		log.Fatal(err)
	}
}

func (a *app) installMenu() {
	a.win.SetMenu(mygo.NewMenu([]*mygo.MenuItem{{Label: a.tr("menu.file"), Submenu: []*mygo.MenuItem{{Label: a.tr("menu.settings"), Click: func(_ *mygo.MenuItem, _ *mygo.Window) {
		if !a.refreshing {
			a.openSettings()
			a.win.Invalidate()
		}
	}}, mygo.Separator(), {Label: a.tr("menu.exit"), Click: func(_ *mygo.MenuItem, _ *mygo.Window) {
		a.win.Close()
	}}}}, {Label: a.tr("menu.view"), Submenu: []*mygo.MenuItem{
		{Label: a.tr("menu.theme"), Submenu: a.themeMenuItems()},
		{Label: a.tr("menu.language"), Submenu: a.languageMenuItems()},
	}}, {Label: a.tr("menu.help"), Submenu: []*mygo.MenuItem{
		{Label: a.tr("about.about_webhola"), Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.openAbout() }},
	}}}))
}
