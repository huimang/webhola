package desktop

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"

	capturepkg "webhola/native/internal/capture"
	i18npkg "webhola/native/internal/i18n"
	jvmpkg "webhola/native/internal/jvm"
)

func (a *app) arthasView(c *ui.Context) {
	a.applyTheme(c)
	s := a.arthas
	if s == nil {
		return
	}
	ui.Column(c).FillWidth().FillHeight().Gap(0).Children(func() {
		ui.Column(c).FillWidth().Padding(12, 20).Gap(8).Children(func() {
			ui.Text(c, a.tr("arthas_ui.jvm_routes_and_tracing")).FontSize(18).Bold()
			ui.Row(c).FillWidth().Gap(16).Children(func() {
				ui.Column(c.Key("jvm-controls")).Grow(1).Gap(8).Children(func() { a.arthasControls(c, s) })
				if s.target.PID != 0 {
					ui.Column(c.Key("jvm-process-summary")).Width(400).Children(func() { a.jvmProcessView(c, s) })
				}
			})
			if s.status != "" {
				ui.Text(c, a.tr(s.status)).FontSize(12).Role(ui.RoleStatus).TextColor(c.Theme().Accent).Selectable()
			}
		})
		ui.Box(c).FillWidth().Height(1).Background(c.Theme().Border)
		ui.Row(c).FillWidth().Grow(1).Gap(1).Background(c.Theme().Border).Children(func() {
			ui.Column(c).Width(350).FillHeight().Padding(16).Gap(10).Background(c.Theme().Surface).Children(func() { a.arthasCallList(c, s) })
			ui.Column(c).Grow(1).FillHeight().Padding(20).Gap(12).Background(c.Theme().Surface).Children(func() { a.arthasCallDetails(c, s) })
		})
		ui.Text(c, a.tr("arthas_monitor.method_scope_notice")).FontSize(11).TextColor(c.Theme().TextMuted).Padding(10, 20)
	})
}

func (a *app) arthasControls(c *ui.Context, s *arthasState) {
	unavailable := s.memory.Process.State == "exited" || s.memory.Process.State == "replaced"
	ui.Row(c).FillWidth().Gap(8).Children(func() {
		ui.TextInput(c.Key("route-search"), &s.search).Width(190).Label(a.tr("arthas_ui.search_routes")).Placeholder(a.tr("arthas_ui.search_routes")).Tooltip(a.tr("arthas_ui.search_url_http_method_class_or_method")).Disabled(s.busy)
		selected := s.routeChoice
		label := ""
		for _, r := range s.routes {
			if jvmpkg.RouteKey(r) == selected {
				label = r.Mapping
			}
		}
		selectRoute := ui.SelectBase(c.Key("jvm-route-select"), &selected)
		theme := c.Theme()
		selectRoute.Trigger.Grow(1).Height(32).Padding(6, 10).Gap(8).
			Background(theme.Surface).Border(1, theme.Border).Radius(theme.Radius).
			Label(a.tr("arthas_monitor.route_select")).Disabled(s.busy || len(s.routes) == 0).
			Children(func() {
				ui.Row(c).FillWidth().Gap(8).Children(func() {
					ui.Text(c, label).Grow(1).SingleLine()
					ui.Text(c, "⌄").TextColor(theme.TextMuted)
				})
			})
		selectRoute.Popup(func(panel ui.Element) {
			panel.Padding(4).Background(theme.Surface).Border(1, theme.Border).Radius(theme.Radius)
			ui.Scroll(c).MaxHeight(320).Children(func() {
				ui.Column(c).FillWidth().Children(func() {
					for _, r := range s.routes {
						if !routeMatches(r, s.search) {
							continue
						}
						item := selectRoute.Item(jvmpkg.RouteKey(r)).Padding(6, 10).Radius(theme.Radius)
						if item.Highlighted() {
							item.Background(theme.Accent).TextColor(theme.AccentText)
						}
						item.Children(func() { ui.Text(c, r.Mapping).SingleLine() })
					}
				})
			})
		})
		if selectRoute.Trigger.Changed() {
			s.selectRoute(selected)
		}
		scanLabel := "arthas_ui.attach_and_scan_routes"
		if s.connection() != nil {
			scanLabel = "arthas_ui.rescan_routes"
		}
		if ui.PrimaryButton(c, a.tr(scanLabel)).Disabled(s.busy || !s.target.JVM || unavailable).Clicked() {
			a.scanArthasRoutes(s)
		}
		if ui.Button(c, a.tr("arthas_ui.disconnect")).Disabled(s.busy || s.connection() == nil).Clicked() {
			a.disconnectArthas(s)
		}
	})
	ui.Row(c).FillWidth().Gap(8).Children(func() {
		ui.TextInput(c.Key("java-class"), &s.method.ClassName).Grow(1).Label(a.tr("arthas_ui.target_class")).Placeholder(a.tr("arthas_ui.fully_qualified_class_name")).Disabled(s.busy)
		ui.TextInput(c.Key("java-method"), &s.method.MethodName).Width(140).Label(a.tr("arthas_ui.target_method")).Placeholder(a.tr("arthas_ui.method_name")).Disabled(s.busy)
		ui.Select(c.Key("java-loader"), &s.method.Loader, s.methodLoaders()).Width(140).Label(a.tr("arthas_monitor.classloader")).Disabled(s.busy)
	})
	ui.Row(c).FillWidth().Gap(8).Children(func() {
		if ui.Button(c, a.tr("arthas_ui.watch")).Disabled(s.busy || s.connection() == nil || s.method.MethodName == "" || unavailable).Clicked() {
			a.observeArthas(s, "watch")
		}
		if ui.Button(c, a.tr("arthas_ui.trace")).Disabled(s.busy || s.connection() == nil || s.method.MethodName == "" || unavailable).Clicked() {
			a.observeArthas(s, "trace")
		}
		if ui.Button(c, a.tr("arthas_ui.stop_operation")).Disabled(!s.busy).Clicked() {
			if s.opCancel != nil {
				s.opCancel()
			}
		}
	})
}

func (a *app) arthasCallList(c *ui.Context, s *arthasState) {
	ui.Row(c).FillWidth().Gap(8).Children(func() {
		ui.Text(c, a.tr("arthas_monitor.calls")).FontSize(14).Bold()
		ui.Box(c).Grow(1)
		if ui.Button(c, a.tr("main.clear")).Clicked() {
			s.clearRecords()
		}
	})
	count := 0
	for _, r := range s.records {
		if s.routeChoice == "" || r.RouteKey == s.routeChoice {
			count++
		}
	}
	ui.Textf(c, a.tr("arthas_monitor.record_count"), count, len(s.records), jvmpkg.MaxArthasRecords).FontSize(12).TextColor(c.Theme().TextMuted)
	ui.Scroll(c.Key("arthas-calls")).FillWidth().Grow(1).Children(func() {
		ui.Column(c).FillWidth().Gap(8).Children(func() {
			if count == 0 {
				ui.Text(c, a.tr("arthas_monitor.empty_calls")).TextColor(c.Theme().TextMuted)
			}
			for i := len(s.records) - 1; i >= 0; i-- {
				r := s.records[i]
				if s.routeChoice != "" && r.RouteKey != s.routeChoice {
					continue
				}
				bg := c.Theme().Surface
				if r.ID == s.selectedRecord {
					bg = c.Theme().Selection
				}
				ui.Column(c.Key(fmt.Sprintf("arthas-call-%d", r.ID))).FillWidth().Padding(12).Gap(5).Radius(8).Border(1, c.Theme().Border).Background(bg).OnClick(func() { s.selectedRecord = r.ID; s.preview = nil; s.detailTab = "" }).Label(fmt.Sprintf("%s #%d", strings.ToUpper(r.Kind), r.ID)).Children(func() {
					ui.Row(c).FillWidth().Gap(8).Children(func() {
						ui.Text(c, strings.ToUpper(r.Kind)).Bold().TextColor(c.Theme().Accent)
						ui.Text(c, fmt.Sprintf("#%d", r.ID)).FontSize(11).TextColor(c.Theme().TextMuted)
						ui.Box(c).Grow(1)
						status := "arthas_monitor.completed"
						color := c.Theme().TextMuted
						if r.Failed {
							status = "arthas_monitor.exception"
							color = c.Theme().Danger
						}
						ui.Text(c, a.tr(status)).FontSize(12).TextColor(color)
					})
					ui.Text(c, r.Method.ClassName+"#"+r.Method.MethodName).FontSize(12)
					ui.Text(c, r.RouteLabel).FontSize(11).TextColor(c.Theme().TextMuted)
					ui.Text(c, r.Summary()).FontSize(12).TextColor(c.Theme().TextMuted)
				})
			}
		})
	})
}

func (a *app) arthasCallDetails(c *ui.Context, s *arthasState) {
	r := s.selectedCall()
	if r == nil {
		ui.Text(c, a.tr("arthas_monitor.select_call")).FontSize(18).Bold()
		ui.Text(c, a.tr("arthas_monitor.detail_hint")).TextColor(c.Theme().TextMuted)
		return
	}
	name := r.Method.ClassName + "#" + r.Method.MethodName
	ui.Row(c).FillWidth().Gap(6).Children(func() {
		ui.Text(c, name).FontSize(16).Bold().Selectable().Shrink(1)
		a.javaSourceIcon(c, s, r.Method, "source-call-class", 0)
		ui.Box(c).Grow(1)
	})
	ui.Text(c, r.RouteLabel).FontSize(12).TextColor(c.Theme().TextMuted).Selectable()
	ui.Text(c, strings.ToUpper(r.Kind)+" · "+r.Summary()).FontSize(12).TextColor(c.Theme().TextMuted)
	if s.detailTab == "" {
		if r.Kind == "trace" {
			s.detailTab = "tree"
		} else if r.Structured {
			s.detailTab = "result"
		} else {
			s.detailTab = "raw"
		}
	}
	if r.Truncated {
		ui.Text(c, a.tr("arthas_monitor.truncated")).FontSize(12).TextColor(c.Theme().Warning)
	}
	if r.Kind == "watch" && !r.Structured {
		ui.Text(c, a.tr("arthas_monitor.unstructured")).FontSize(12).TextColor(c.Theme().Warning)
	}
	ui.Row(c).FillWidth().Gap(8).Children(func() {
		var tabs []struct{ id, key string }
		if r.Kind == "trace" {
			tabs = []struct{ id, key string }{{"tree", "arthas_monitor.call_tree"}, {"raw", "arthas_monitor.raw_result"}}
		} else {
			tabs = []struct{ id, key string }{{"params", "arthas_monitor.parameters"}, {"result", "arthas_monitor.return_value"}, {"exception", "arthas_monitor.exception"}, {"raw", "arthas_monitor.raw_result"}}
		}
		for _, tab := range tabs {
			if ui.Button(c, a.tr(tab.key)).Selected(s.detailTab == tab.id).Disabled(r.Kind == "watch" && !r.Structured && tab.id != "raw").Clicked() {
				s.detailTab = tab.id
				s.preview = nil
			}
		}
	})
	if s.detailTab == "tree" {
		nodes := jvmpkg.VisibleTraceMethods(r.Chain)
		ui.Scroll(c.Key(fmt.Sprintf("trace-tree-%d", r.ID))).FillWidth().Grow(1).Children(func() {
			ui.Column(c).FillWidth().Gap(8).Children(func() {
				if len(nodes) == 0 {
					ui.Text(c, a.tr("arthas_monitor.no_tree")).TextColor(c.Theme().TextMuted)
				}
				for i, node := range nodes {
					ui.Row(c.Key(fmt.Sprintf("trace-node-%d-%d", r.ID, i))).FillWidth().Padding(8).Gap(8).Border(1, c.Theme().Border).Radius(6).Children(func() {
						name := strings.Repeat("  ", min(node.Depth, 8)) + node.ClassName + "#" + node.MethodName
						if node.Line > 0 {
							name += fmt.Sprintf(":%d", node.Line)
						}
						color := c.Theme().Text
						if node.Failed {
							color = c.Theme().Danger
						}
						ui.Text(c, name).FontSize(12).TextColor(color).Selectable().Shrink(1)
						target, line := traceSourceLocation(node, r.Method)
						a.javaSourceIcon(c, s, target, fmt.Sprintf("source-trace-%d-%d", r.ID, i), line)
						ui.Box(c).Grow(1)
						ui.Text(c, fmt.Sprintf("%.3f ms · ×%d", node.Cost, max(node.Times, 1))).FontSize(11).TextColor(c.Theme().TextMuted)
						if ui.Button(c.Key(fmt.Sprintf("drill-%d-%d", r.ID, i)), a.tr("arthas_ui.drill_down")).Disabled((s.busy && !s.observing) || s.pendingDrill != nil || s.connection() == nil || !jvmpkg.JavaClassName.MatchString(node.ClassName) || !jvmpkg.JavaMethodName.MatchString(node.MethodName)).Clicked() {
							a.drillArthas(s, node.JavaMethod)
						}
					})
				}
			})
		})
		return
	}
	text := r.Raw
	switch s.detailTab {
	case "params":
		text = r.Params
		if r.DecompiledParams {
			ui.Text(c, a.tr("arthas_monitor.decompiled_parameters")).FontSize(11).TextColor(c.Theme().TextMuted)
		}
		if r.UnnamedParams {
			ui.Text(c, a.tr("arthas_monitor.unnamed_parameters")).FontSize(12).TextColor(c.Theme().Warning)
		}
	case "result":
		text = r.Result
	case "exception":
		text = r.Exception
	}
	key := fmt.Sprintf("arthas-payload-%d-%s", r.ID, s.detailTab)
	if s.preview == nil || s.preview.key != key {
		contentType := "application/json"
		if !json.Valid([]byte(text)) {
			contentType = "text/plain"
		}
		partKey := "arthas_monitor.raw_result"
		switch s.detailTab {
		case "params":
			partKey = "arthas_monitor.parameters"
		case "result":
			partKey = "arthas_monitor.return_value"
		case "exception":
			partKey = "arthas_monitor.exception"
		}
		title := i18npkg.MessageText("arthas_monitor.payload_title", r.Method.ClassName+"#"+r.Method.MethodName, i18npkg.MessageText(partKey))
		s.preview = newBodyPreview(key, &capturepkg.Message{Line: title, Body: text, ContentType: contentType})
		s.preview.formatted = s.preview.pretty
		s.preview.parent = a.win
		s.preview.labelKey = "arthas_monitor.payload"
		s.preview.fullscreenTitleKey = "arthas_monitor.fullscreen"
	}
	a.bodyToolbar(c, s.preview, false)
	a.renderBody(c, s.preview)
}

func (s *arthasState) methodLoaders() []string {
	seen := map[string]bool{}
	for _, hash := range s.loaderChoices {
		seen[hash] = true
	}
	for _, r := range s.routes {
		if r.ClassName == s.method.ClassName && r.Loader != "" {
			seen[r.Loader] = true
		}
	}
	if s.method.Loader != "" {
		seen[s.method.Loader] = true
	}
	choices := make([]string, 0, len(seen))
	for hash := range seen {
		choices = append(choices, hash)
	}
	sort.Strings(choices)
	return choices
}
