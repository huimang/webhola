package desktop

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/egoist/mygo/ui"

	i18npkg "webhola/native/internal/i18n"
	jvmpkg "webhola/native/internal/jvm"
)

func TestLanguagePersistencePreservesDependencies(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := settingsPath()
	if err != nil {
		t.Fatal(err)
	}
	want := settings{TSharkPath: `C:\missing\tshark.exe`, ArthasConfig: jvmpkg.ArthasConfig{JavaPath: `C:\missing\java.exe`, BootPath: `C:\missing\arthas-boot.jar`, HTTPPort: "9876"}}
	if err := writeSettings(path, want); err != nil {
		t.Fatal(err)
	}
	a := newApp(context.Background())
	a.changeLanguage("en")
	got, err := readSettings(path)
	want.Language = "en"
	if err != nil || got != want || a.language != "en" {
		t.Fatalf("language save changed dependencies: %+v / %v", got, err)
	}
	got.JavaPath, got.BootPath = "", ""
	if err := writeSettings(path, got); err != nil {
		t.Fatal(err)
	}
	if err := persistSettings(context.Background(), path, ""); err != nil {
		t.Fatal(err)
	}
	got, err = readSettings(path)
	if err != nil || got.Language != "en" {
		t.Fatal("dependency save lost language")
	}
	a.changeLanguage("zh-CN")
	got, err = readSettings(path)
	if err != nil || got.Language != "zh-CN" || a.tr("http.request") != "请求" {
		t.Fatal("could not switch back to Chinese")
	}
	for _, language := range []string{"", "fr", "zh-CN"} {
		if i18npkg.NormalizeLanguage(language) != "zh-CN" {
			t.Fatal("unknown language must fall back to Chinese")
		}
	}
}

func TestLanguageSaveFailureKeepsSelection(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, _ := settingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("invalid json"), 0600); err != nil {
		t.Fatal(err)
	}
	a := newApp(context.Background())
	a.changeLanguage("en")
	if i18npkg.NormalizeLanguage(a.language) != "zh-CN" || !a.failed {
		t.Fatal("failed save must retain current language and report error")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "invalid json" {
		t.Fatal("failed language save overwrote settings")
	}
}

func TestEnglishMessagesAndCatalogCoverage(t *testing.T) {
	a := newApp(context.Background())
	a.language = "en"
	for _, pair := range [][2]string{
		{i18npkg.MessageText("service_scan.probing_http_services_d_d", 3, 10), "Probing HTTP services: 3 / 10"},
		{i18npkg.MessageText("replay.resend_complete_http_d", 201), "Resend complete. HTTP 201."},
		{i18npkg.MessageText("settings.tshark_path_is_unavailable_w", fmt.Errorf("file missing")), "TShark path is unavailable: file missing"},
	} {
		if got := a.tr(pair[0]); got != pair[1] {
			t.Errorf("%q => %q, want %q", pair[0], got, pair[1])
		}
	}
	var files []string
	err := filepath.WalkDir("..", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			if literal, ok := node.(*ast.BasicLit); ok && literal.Kind == token.STRING {
				text, _ := strconv.Unquote(literal.Value)
				if strings.ContainsFunc(text, func(r rune) bool { return unicode.Is(unicode.Han, r) }) {
					t.Errorf("hardcoded Chinese in %s: %q", path, text)
				}
			}
			if call, ok := node.(*ast.CallExpr); ok && len(call.Args) > 0 {
				name := ""
				switch fn := call.Fun.(type) {
				case *ast.Ident:
					name = fn.Name
				case *ast.SelectorExpr:
					name = fn.Sel.Name
				}
				if name == "tr" || name == "MessageText" || name == "MessageError" {
					if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						key, _ := strconv.Unquote(lit.Value)
						if _, ok := i18npkg.MessageCatalogs["zh-CN"][key]; !ok {
							t.Errorf("unknown message key in %s: %s", path, key)
						}
					}
				}
			}
			return true
		})
	}
	verbs := regexp.MustCompile(`%[-+# .0-9]*[a-zA-Z]`)
	for language, catalog := range i18npkg.MessageCatalogs {
		if len(catalog) != len(i18npkg.MessageCatalogs["zh-CN"]) {
			t.Errorf("catalog size differs: %s", language)
		}
		for key, source := range i18npkg.MessageCatalogs["zh-CN"] {
			target, ok := catalog[key]
			if !ok || target == "" {
				t.Errorf("missing resource %s / %s", language, key)
			}
			if strings.Join(verbs.FindAllString(source, -1), ",") != strings.Join(verbs.FindAllString(target, -1), ",") {
				t.Errorf("format mismatch: %s / %s", language, key)
			}
		}
	}
}

func TestLocalizedMessagesPreserveArgumentsAndErrorChain(t *testing.T) {
	a := newApp(context.Background())
	err := i18npkg.MessageError("settings.tshark_path_is_unavailable_w", context.DeadlineExceeded)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("localized error lost its cause")
	}
	status := i18npkg.MessageText("service_scan.scan_failed") + i18npkg.ErrorMessage(err)
	a.language = "en"
	if got := a.tr(status); got != "Scan failed: TShark path is unavailable: context deadline exceeded" {
		t.Fatal(got)
	}
	a.language = "zh-CN"
	if got := a.tr(status); got != "扫描失败：TShark 路径不可用：context deadline exceeded" {
		t.Fatal(got)
	}
	// Translation applies to message descriptors, never to source-language text
	// passed in as data, including words identical to a translated UI label.
	a.language = "en"
	value := "请求 保存 响应 / 中文路径"
	if got := a.tr(i18npkg.MessageText("settings.tshark_path_is_unavailable_w", value)); got != "TShark path is unavailable: "+value {
		t.Fatal(got)
	}
	if got := a.tr(value); got != value {
		t.Fatal("raw data was translated", got)
	}
	if got := i18npkg.TranslateText("unsupported", "common.save"); got != "保存" {
		t.Fatal("fallback failed", got)
	}
	if got := a.tr(i18npkg.MessageText("service_scan.probing_http_services_d_d", -3, uint64(10))); got != "Probing HTTP services: -3 / 10" {
		t.Fatal(got)
	}
}

func TestEnglishViewsAndPayloadPreservation(t *testing.T) {
	a := newApp(context.Background())
	a.language = "en"
	a.loadDemo()
	a.tab = "http.response"
	payload := `{"请求":"响应","note":"保存"}`
	a.entries[0].Response.Body = payload
	tt := ui.NewTester(a.view, 1200, 820)
	for _, label := range []string{"Requests", "Scan HTTP services", "Start capture", "Raw", "Format"} {
		if !tt.HasText(label) {
			t.Errorf("missing English control %s", label)
		}
	}
	if a.bodyPreview.rawText != payload {
		t.Fatal("translation changed HTTP payload")
	}
	if err := tt.Click("Request"); err != nil {
		t.Fatal(err)
	}
	if a.tab != "http.request" {
		t.Fatal("translated tab changed internal identity")
	}
	if err := tt.Click("Response"); err != nil {
		t.Fatal(err)
	}
	if a.tab != "http.response" {
		t.Fatal("translated response tab failed")
	}
	if err := tt.Click("HTTP body"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Key(ui.Cmd, ui.KeyC)
	if tt.Clipboard() != payload {
		t.Fatalf("copy changed payload: %q", tt.Clipboard())
	}
	a.openSettings()
	settings := ui.NewTester(a.settingsView, 820, 700)
	for _, label := range []string{"TShark location", "Save", "Cancel"} {
		if !settings.HasText(label) {
			t.Errorf("missing settings label %s", label)
		}
	}
	if !ui.NewTester(a.aboutView, 560, 420).HasText("Features") {
		t.Fatal("about dialog not translated")
	}
	a.openBodyFullscreen(a.bodyPreview)
	if !ui.NewTester(a.bodyFullscreenView, 1200, 820).HasText("Exit full screen") {
		t.Fatal("full screen not translated")
	}
	if preview := os.Getenv("WEBHOLA_I18N_PREVIEW"); preview != "" {
		for _, item := range []struct {
			name          string
			view          func(*ui.Context)
			width, height int
		}{
			{"main-en", a.view, 1200, 820}, {"settings-en", a.settingsView, 820, 700}, {"about-en", a.aboutView, 560, 420},
		} {
			file, err := os.Create(filepath.Join(preview, fmt.Sprintf("%s.png", item.name)))
			if err != nil {
				t.Fatal(err)
			}
			err = png.Encode(file, ui.NewTester(item.view, item.width, item.height).Image())
			file.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
	}
}
