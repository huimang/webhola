package desktop

import (
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	i18npkg "webhola/native/internal/i18n"
)

type loadingDialog struct {
	window           *mygo.Window
	active, stopping bool
	title, hint      string
	status           func() string
	cancel           func()
	completed, total int
}

func (a *app) openLoading(parent *mygo.Window, title, hint string, status func() string, cancel func(), steps ...int) *loadingDialog {
	d := &loadingDialog{active: true, title: title, hint: hint, status: status, cancel: cancel}
	if len(steps) > 0 {
		d.total = steps[0]
	}
	if parent == nil {
		return d
	}
	w := mygo.NewWindow(mygo.WindowOptions{Title: a.tr(title), Width: 560, Height: 330, Parent: parent, Modal: true, DisableMinimize: true, DisableMaximize: true, Content: ui.View(func(c *ui.Context) { a.loadingView(c, d) })})
	d.window = w
	_ = w.SetIcon(windowIcon)
	w.OnClose(func(e *mygo.CloseEvent) {
		if d.active {
			e.PreventDefault()
			d.stop()
		}
	})
	w.OnClosed(func() { d.window = nil })
	w.Show()
	return d
}

func (d *loadingDialog) stop() {
	if d == nil || !d.active || d.stopping {
		return
	}
	d.stopping = true
	if d.cancel != nil {
		d.cancel()
	}
	if d.window != nil {
		d.window.Invalidate()
	}
}

func (d *loadingDialog) close() {
	if d == nil {
		return
	}
	d.active = false
	if d.window != nil {
		d.window.Close()
	}
}

func (a *app) loadingView(c *ui.Context, d *loadingDialog) {
	a.applyTheme(c)
	c.After(100 * time.Millisecond)
	ui.Column(c).FillWidth().FillHeight().Padding(24).Gap(16).Children(func() {
		ui.Row(c).Gap(12).Children(func() {
			ui.Spinner(c).Label(a.tr(d.title))
			ui.Text(c, a.tr(d.title)).FontSize(18).Bold()
		})
		status := ""
		if d.status != nil {
			status = d.status()
		}
		if d.stopping {
			status = i18npkg.MessageText("main.stopping")
		}
		ui.Text(c, a.tr(status)).Role(ui.RoleStatus)
		if d.total > 0 {
			a.loadingProgress(c, d)
			ui.Text(c, a.tr("loading.stage_progress_hint")).FontSize(12).TextColor(c.Theme().TextMuted)
		}
		ui.Text(c, a.tr(d.hint)).TextColor(c.Theme().TextMuted)
		ui.Box(c).Grow(1)
		ui.Row(c).FillWidth().Children(func() {
			ui.Box(c).Grow(1)
			if ui.Button(c, a.tr("common.cancel")).Disabled(d.stopping).Clicked() {
				d.stop()
			}
		})
	})
}

func (a *app) loadingProgress(c *ui.Context, d *loadingDialog) {
	if d == nil || d.total <= 0 {
		return
	}
	completed := min(max(d.completed, 0), d.total)
	percent := completed * 100 / d.total
	ui.Text(c, a.tr(i18npkg.MessageText("loading.stage_progress", percent, completed, d.total))).FontSize(12).Bold()
	// Use regular layout surfaces here: the native popup renderer does not
	// consistently paint the Progress widget's custom drawing.
	ui.Box(c).FillWidth().Height(6).Radius(3).Background(c.Theme().Border).
		Role(ui.RoleProgress).Range(0, float64(d.total), float64(completed)).
		Label(a.tr("loading.progress_label")).Children(func() {
		if completed > 0 {
			ui.Box(c).WidthPercent(float32(completed) * 100 / float32(d.total)).Height(6).Radius(3).Background(c.Theme().Accent)
		}
	})
}
