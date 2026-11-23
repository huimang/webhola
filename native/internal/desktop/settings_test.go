package desktop

import (
	"context"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	jvmpkg "webhola/native/internal/jvm"
)

func TestSettingsPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Webhola", "settings.json")
	s, err := readSettings(path)
	if err != nil || s != (settings{}) {
		t.Fatal("missing settings should use defaults")
	}
	want := settings{Language: "en", ThemeMode: "dark", ArthasConfig: jvmpkg.ArthasConfig{JavaPath: `D:\tools with spaces\bin\java.exe`, HTTPPort: "9876"}}
	if err := writeSettings(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := readSettings(path)
	if err != nil || got != want {
		t.Fatalf("settings not retained: %+v %v", got, err)
	}
	if err := os.WriteFile(path, []byte("invalid json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSettings(path); err == nil {
		t.Fatal("malformed settings accepted")
	}
}

func TestSettingsViewCancelAndBusy(t *testing.T) {
	a := newApp(context.Background())
	a.arthasConfig = jvmpkg.ArthasConfig{JavaPath: `C:\tools\java.exe`}
	a.openSettings()
	tt := ui.NewTester(a.settingsView, 740, 460)
	if preview := os.Getenv("WEBHOLA_SETTINGS_PREVIEW"); preview != "" {
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
	if !tt.HasText("Java / Arthas") || a.arthasDraft.JavaPath != a.arthasConfig.JavaPath {
		t.Fatal("settings not populated")
	}
	for _, size := range [][2]int{{740, 460}, {640, 400}} {
		tt.SetSize(size[0], size[1])
		for _, label := range []string{"保存", "取消"} {
			r, ok := tt.Find(label)
			if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0]) || r.Y+r.H > float32(size[1]) {
				t.Fatalf("%s outside dialog at %v: %+v", label, size, r)
			}
		}
	}
	a.arthasDraft.JavaPath = "unsaved.exe"
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	if a.showSettings || a.arthasConfig.JavaPath != `C:\tools\java.exe` {
		t.Fatal("cancel saved changes")
	}
	a.running = true
	a.openSettings()
	if a.showSettings || !strings.Contains(a.tr(a.status), "先停止监听") {
		t.Fatal("settings opened during capture")
	}
}

func TestInvalidSettingsKeepSavedValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	want := settings{Language: "en", ThemeMode: "light", ArthasConfig: jvmpkg.ArthasConfig{HTTPPort: "9876"}}
	if err := writeSettings(path, want); err != nil {
		t.Fatal(err)
	}
	bad := settings{ArthasConfig: jvmpkg.ArthasConfig{JavaPath: filepath.Join(t.TempDir(), "missing.exe")}}
	if err := persistAllSettings(context.Background(), path, bad); err == nil {
		t.Fatal("invalid Java path saved")
	}
	got, err := readSettings(path)
	if err != nil || got != want {
		t.Fatal("failed validation changed existing settings")
	}
}

func TestSaveSettingsAndReopen(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	a := newApp(context.Background())
	a.pcap = "fixture.pcap"
	a.arthasConfig = jvmpkg.ArthasConfig{HTTPPort: "9876"}
	a.openSettings()
	queue := make(chan func(), 4)
	a.dispatch = func(fn func()) { queue <- fn }
	tt := ui.NewTester(a.settingsView, 740, 460)
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	select {
	case fn := <-queue:
		fn()
	case <-time.After(15 * time.Second):
		t.Fatal("save did not finish")
	}
	a.workers.Wait()
	if a.showSettings || a.settingsBusy || a.arthasConfig.HTTPPort != "9876" {
		t.Fatalf("save failed: %s", a.settingsStatus)
	}
	path, err := settingsPath()
	if err != nil {
		t.Fatal(err)
	}
	saved, err := readSettings(path)
	if err != nil || saved.ArthasConfig != a.arthasConfig || saved.Language != "zh-CN" {
		t.Fatalf("saved settings were not readable: %+v %v", saved, err)
	}
	a.openSettings()
	if a.arthasDraft != a.arthasConfig {
		t.Fatal("reopening lost saved values")
	}
}
