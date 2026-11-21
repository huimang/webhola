package desktop

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	i18npkg "webhola/native/internal/i18n"
)

const appVersion = "0.1.0"

func (a *app) openAbout() {
	if a.aboutWindow != nil {
		a.aboutWindow.Focus()
		return
	}
	w := mygo.NewWindow(mygo.WindowOptions{Title: a.tr("about.about_webhola"), Width: 560, Height: 420, MinWidth: 500, MinHeight: 380, Parent: a.win, Modal: true, DisableMinimize: true, DisableMaximize: true, Content: ui.View(a.aboutView)})
	a.aboutWindow = w
	_ = w.SetIcon(windowIcon)
	w.OnClosed(func() { a.aboutWindow = nil })
	w.Show()
}

func (a *app) aboutView(c *ui.Context) {
	a.applyTheme(c)
	ui.Column(c).FillWidth().FillHeight().Padding(24).Gap(16).Children(func() {
		ui.Scroll(c).FillWidth().Grow(1).Children(func() {
			ui.Column(c).FillWidth().Gap(14).Children(func() {
				ui.Row(c).FillWidth().Gap(14).Children(func() {
					ui.Image(c, appIcon).Width(60).Height(60).Label(a.tr("about.webhola_icon"))
					ui.Column(c).Gap(4).Children(func() {
						ui.Text(c, a.tr("common.product_name")).FontSize(26).Bold()
						ui.Text(c, a.tr(i18npkg.MessageText("about.version")+appVersion)).TextColor(c.Theme().TextMuted)
					})
				})
				ui.Text(c, a.tr("about.a_local_toolkit_for_web_developers_to"))
				ui.Text(c, a.tr("about.features")).Bold()
				ui.Text(c, a.tr("about.http_service_discovery_port_and_adapter_selection")).TextColor(c.Theme().TextMuted)
				ui.Text(c, a.tr("about.powered_by_tshark_supports_plain_http_1")).FontSize(12).TextColor(c.Theme().TextMuted)
			})
		})
		ui.Box(c).FillWidth().Height(1).Background(c.Theme().Border)
		ui.Row(c).FillWidth().Height(40).Children(func() {
			ui.Box(c).Grow(1)
			if ui.PrimaryButton(c, a.tr("common.close")).MinWidth(96).Clicked() {
				if a.aboutWindow != nil {
					a.aboutWindow.Close()
				}
			}
		})
	})
}
