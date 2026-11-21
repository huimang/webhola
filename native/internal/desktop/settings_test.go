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

	capturepkg "webhola/native/internal/capture"
)

func TestSettingsPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Webhola", "settings.json")
	s, err := readSettings(path)
	if err != nil || s.TSharkPath != "" {
		t.Fatal("missing settings should use auto discovery")
	}
	for _, want := range []string{`D:\tools with spaces\Wireshark\tshark.exe`, ""} {
		if err := writeSettings(path, settings{TSharkPath: want}); err != nil {
			t.Fatal(err)
		}
		got, err := readSettings(path)
		if err != nil || got.TSharkPath != want {
			t.Fatalf("settings not retained: %+v %v", got, err)
		}
	}
	if err := os.WriteFile(path, []byte("invalid json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSettings(path); err == nil {
		t.Fatal("malformed settings accepted")
	}
}

func TestConfiguredPathOverridesEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tshark.exe")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WEBHOLA_TSHARK", filepath.Join(dir, "missing.exe"))
	got, err := capturepkg.ConfiguredTShark(`"` + path + `"`)
	if err != nil || got != path {
		t.Fatalf("configured path was not preferred: %q %v", got, err)
	}
	for _, bad := range []string{dir, filepath.Join(dir, "missing.exe"), "relative.exe"} {
		if _, err := capturepkg.ConfiguredTShark(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestSettingsViewCancelAndBusy(t *testing.T) {
	a := newApp(context.Background())
	a.tsharkPath = `C:\tools\tshark.exe`
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
	if !tt.HasText("TShark 位置") || a.settingsDraft != a.tsharkPath {
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
	a.settingsDraft = "unsaved.exe"
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	if a.showSettings || a.tsharkPath != `C:\tools\tshark.exe` {
		t.Fatal("cancel saved changes")
	}
	a.running = true
	a.openSettings()
	if a.showSettings || !strings.Contains(a.tr(a.status), "先停止监听") {
		t.Fatal("settings opened during capture")
	}
}

func TestInvalidSettingsKeepSavedPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	want := settings{TSharkPath: "previous.exe"}
	if err := writeSettings(path, want); err != nil {
		t.Fatal(err)
	}
	if err := persistSettings(context.Background(), path, filepath.Join(t.TempDir(), "missing.exe")); err == nil {
		t.Fatal("invalid path saved")
	}
	got, err := readSettings(path)
	if err != nil || got != want {
		t.Fatal("failed validation changed existing settings")
	}
}

func TestSaveSettingsAndReopenIntegration(t *testing.T) {
	if os.Getenv("WEBHOLA_INTEGRATION") != "1" {
		t.Skip("requires real TShark")
	}
	tool, err := capturepkg.FindTShark()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	a := newApp(context.Background())
	a.pcap = "fixture.pcap"
	a.openSettings()
	a.settingsDraft = tool
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
	if a.showSettings || a.settingsBusy || a.tsharkPath != tool {
		t.Fatalf("save failed: %s", a.settingsStatus)
	}
	select {
	case fn := <-queue:
		fn()
	default:
		t.Fatal("main window was not refreshed")
	}
	path, err := settingsPath()
	if err != nil {
		t.Fatal(err)
	}
	saved, err := readSettings(path)
	if err != nil || saved.TSharkPath != tool {
		t.Fatalf("saved path was not readable: %+v %v", saved, err)
	}
	a.openSettings()
	if a.settingsDraft != tool {
		t.Fatal("reopening lost saved path")
	}
	if err := validateTShark(context.Background(), saved.TSharkPath); err != nil {
		t.Fatal(err)
	}
}
