package desktop

import (
	"context"
	"errors"
	"image/png"
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	i18npkg "webhola/native/internal/i18n"
)

func TestArthasLoadingClosesAfterJob(t *testing.T) {
	for _, result := range []error{nil, errors.New("attach failed"), context.Canceled, context.DeadlineExceeded} {
		t.Run(errorName(result), func(t *testing.T) {
			a := newApp(context.Background())
			updates := make(chan func(), 1)
			a.dispatch = func(fn func()) { updates <- fn }
			s := &arthasState{ctx: a.ctx}
			a.arthas = s
			d := a.openLoading(nil, "loading.routes_title", "loading.routes_hint", func() string { return s.status }, nil)
			s.loading = d
			a.arthasJob(s, time.Second, func(context.Context) error { return result })
			select {
			case fn := <-updates:
				fn()
			case <-time.After(3 * time.Second):
				t.Fatal("job failed to update its loading window")
			}
			a.workers.Wait()
			if d.active || s.busy || s.loading != nil {
				t.Fatal("completed job left loading active")
			}
		})
	}
}

func TestRouteStageProgressAndCancel(t *testing.T) {
	for _, language := range []string{"zh-CN", "en"} {
		a := newApp(context.Background())
		a.language = language
		d := a.openLoading(nil, "loading.routes_title", "loading.routes_hint", func() string { return i18npkg.MessageText("loading.reading_routes") }, nil)
		d.completed, d.total = 3, 5
		tt := ui.NewTester(func(c *ui.Context) { a.loadingView(c, d) }, 560, 330)
		if !tt.HasText(a.tr(i18npkg.MessageText("loading.stage_progress", 60, 3, 5))) {
			t.Fatal("missing phase percentage")
		}
		if preview := os.Getenv("WEBHOLA_LOADING_PREVIEW"); preview != "" && language == "zh-CN" {
			f, err := os.Create(preview)
			if err != nil {
				t.Fatal(err)
			}
			err = png.Encode(f, tt.Image())
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := tt.Click(a.tr("common.cancel")); err != nil {
			t.Fatal(err)
		}
		if d.completed != 3 || !d.stopping || !tt.HasText(a.tr(i18npkg.MessageText("loading.stage_progress", 60, 3, 5))) {
			t.Fatal("cancel must retain the last completed phase")
		}
	}
}

func TestRouteProgressInitializedAndRefreshesInsideDialog(t *testing.T) {
	for _, language := range []string{"zh-CN", "en"} {
		a := newApp(context.Background())
		a.language = language
		d := a.openLoading(nil, "loading.routes_title", "loading.routes_hint", nil, nil, 5)
		tt := ui.NewTester(func(c *ui.Context) { a.loadingView(c, d) }, 560, 330)
		if !tt.HasText(a.tr(i18npkg.MessageText("loading.stage_progress", 0, 0, 5))) {
			t.Fatal("progress missing from the initial dialog frame")
		}
		for _, step := range []int{1, 3, 4} {
			d.completed = step
			tt.Advance(100 * time.Millisecond)
			if !tt.HasText(a.tr(i18npkg.MessageText("loading.stage_progress", step*20, step, 5))) {
				t.Fatal("dialog progress failed to refresh")
			}
		}
	}
}

func errorName(err error) string {
	if err == nil {
		return "success"
	}
	return err.Error()
}

func TestLoadingCancelAndTranslations(t *testing.T) {
	for _, language := range []string{"zh-CN", "en"} {
		a := newApp(context.Background())
		a.language = language
		ctx, cancel := context.WithCancel(a.ctx)
		defer cancel()
		d := a.openLoading(nil, "loading.web_scan_title", "loading.web_scan_hint", func() string { return i18npkg.MessageText("service_scan.probing_http_services_d_d", 3, 10) }, cancel)
		tt := ui.NewTester(func(c *ui.Context) { a.loadingView(c, d) }, 560, 260)
		if !tt.HasText(a.tr("loading.web_scan_title")) || !tt.HasText(a.tr(i18npkg.MessageText("service_scan.probing_http_services_d_d", 3, 10))) {
			t.Fatal("loading window missing translated progress")
		}
		if err := tt.Click(a.tr("common.cancel")); err != nil {
			t.Fatal(err)
		}
		if ctx.Err() == nil || !d.stopping || !d.active {
			t.Fatal("cancel did not stop the job while retaining cleanup progress")
		}
		d.close()
	}
}
