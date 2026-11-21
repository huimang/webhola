package desktop

import (
	"context"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"

	jvmpkg "webhola/native/internal/jvm"
)

func TestThemeModesFollowSystemChanges(t *testing.T) {
	for _, mode := range []string{"", "system", "light", "dark", "unknown"} {
		a := newApp(context.Background())
		a.themeMode = mode
		var got *ui.Theme
		tt := ui.NewTester(func(c *ui.Context) {
			a.applyTheme(c)
			got = c.Theme()
		}, 200, 100)
		for _, systemDark := range []bool{true, false, true} {
			tt.SetDark(systemDark)
			want := systemDark
			if mode == "light" {
				want = false
			} else if mode == "dark" {
				want = true
			}
			if got.Dark != want || got.Background != brandTheme(want).Background {
				t.Fatalf("mode %q system dark %v: %+v", mode, systemDark, got)
			}
		}
	}
}

func TestThemePreferencePreservesOtherSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	want := settings{Language: "en", TSharkPath: "unavailable-tshark.exe", ArthasConfig: jvmpkg.ArthasConfig{JavaPath: "unavailable-java.exe"}}
	if err := writeSettings(path, want); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"dark", "light", "system"} {
		if err := saveThemeMode(path, mode); err != nil {
			t.Fatal(err)
		}
		want.ThemeMode = mode
		got, err := readSettings(path)
		if err != nil || got != want {
			t.Fatalf("theme lost other preferences: %+v %v", got, err)
		}
		if err := saveLanguage(path, "en"); err != nil {
			t.Fatal(err)
		}
		got, err = readSettings(path)
		if err != nil || got != want {
			t.Fatalf("language lost theme preference: %+v %v", got, err)
		}
	}
}

func TestThemeSaveFailureKeepsCurrentMode(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := settingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("invalid json"), 0600); err != nil {
		t.Fatal(err)
	}
	a := newApp(context.Background())
	a.themeMode = "light"
	a.changeThemeMode("dark")
	if a.themeMode != "light" || !a.failed {
		t.Fatal("failed save must retain the current appearance and report an error")
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "invalid json" {
		t.Fatal("failed save overwrote existing settings")
	}
}

func TestThemeChangesRefreshExistingBodyAndWindows(t *testing.T) {
	a := newApp(context.Background())
	a.loadDemo()
	a.tab = "http.response"
	tt := ui.NewTester(a.view, 1200, 820)
	if err := tt.Click("格式化"); err != nil {
		t.Fatal(err)
	}
	p := a.bodyPreview
	if p == nil || p.editor == nil || !p.formatted {
		t.Fatal("formatted JSON missing")
	}
	for _, dark := range []bool{true, false} {
		tt.SetDark(dark)
		if a.bodyPreview != p || !p.formatted || p.editor.Theme.Background != brandTheme(dark).Surface || p.editor.Theme.Text != brandTheme(dark).Text {
			t.Fatal("existing JSON viewer did not follow theme or lost state")
		}
	}
	a.openBodyFullscreen(p)
	for _, view := range []func(*ui.Context){a.settingsView, a.aboutView, a.bodyFullscreenView, a.arthasView} {
		var got bool
		window := ui.NewTester(func(c *ui.Context) {
			view(c)
			got = c.Theme().Dark
		}, 1200, 820)
		window.SetDark(true)
		if !got {
			t.Fatal("secondary window stayed light")
		}
		window.SetDark(false)
		if got {
			t.Fatal("secondary window stayed dark")
		}
	}
}

func TestOptionalThemePreview(t *testing.T) {
	dir := os.Getenv("WEBHOLA_THEME_PREVIEW")
	if dir == "" {
		t.Skip("optional theme preview")
	}
	a := newApp(context.Background())
	a.loadDemo()
	a.tab = "http.response"
	for _, mode := range []string{"light", "dark"} {
		a.themeMode = mode
		tt := ui.NewTester(a.view, 1200, 820)
		if err := tt.Click("格式化"); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(filepath.Join(dir, "theme-"+mode+".png"))
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(f, tt.Image())
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
}
