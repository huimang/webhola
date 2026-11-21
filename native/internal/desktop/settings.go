package desktop

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	capturepkg "webhola/native/internal/capture"
	i18npkg "webhola/native/internal/i18n"
	jvmpkg "webhola/native/internal/jvm"
	platformpkg "webhola/native/internal/platform"
)

type settings struct {
	Language   string `json:"language,omitempty"`
	ThemeMode  string `json:"theme,omitempty"`
	TSharkPath string `json:"tsharkPath"`
	jvmpkg.ArthasConfig
}

func settingsPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "Webhola", "settings.json"), nil
}

func readSettings(path string) (settings, error) {
	var s settings
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	return s, err
}

func writeSettings(path string, s settings) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "settings-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func validateTShark(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	platformpkg.HideProcess(cmd)
	var out platformpkg.TailWriter
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return i18npkg.MessageError("settings.unable_to_run_tshark_check_the_file", err)
	}
	if !strings.Contains(strings.ToLower(string(out.Buf)), "tshark") {
		return i18npkg.MessageError("settings.the_selected_file_did_not_return_tshark")
	}
	return nil
}

func (a *app) openSettings() {
	if a.settingsWindow != nil {
		a.settingsWindow.Focus()
		return
	}
	if a.running {
		a.status = i18npkg.MessageText("settings.stop_capture_before_changing_the_tshark_path")
		return
	}
	a.settingsDraft = a.tsharkPath
	a.arthasDraft = a.arthasConfig
	a.settingsStatus = ""
	a.showSettings = true
	if a.win == nil {
		return
	}
	w := mygo.NewWindow(mygo.WindowOptions{Title: a.tr("i18n.webhola_dependency_settings"), Width: 820, Height: 700, MinWidth: 640, MinHeight: 400, Parent: a.win, Modal: true, DisableMinimize: true, DisableMaximize: true, Content: ui.View(a.settingsView)})
	a.settingsWindow = w
	_ = w.SetIcon(windowIcon)
	w.OnClose(func(e *mygo.CloseEvent) {
		if a.settingsBusy {
			e.PreventDefault()
		}
	})
	w.OnClosed(func() {
		a.settingsWindow = nil
		a.showSettings = false
		a.settingsDraft = a.tsharkPath
		a.win.Invalidate()
	})
	w.Show()
}

func (a *app) closeSettings() {
	if a.settingsBusy {
		return
	}
	if a.settingsWindow != nil {
		a.settingsWindow.Close()
	} else {
		a.showSettings = false
		a.settingsDraft = a.tsharkPath
	}
}

func (a *app) updateSettings(fn func()) {
	if a.ctx.Err() != nil {
		return
	}
	if a.settingsWindow != nil {
		a.settingsWindow.Update(func() {
			if a.ctx.Err() == nil {
				fn()
			}
		})
	} else {
		a.update(fn)
	}
}

func persistSettings(ctx context.Context, path, draft string) error {
	s, err := readSettings(path)
	if err != nil {
		return err
	}
	s.TSharkPath = draft
	return persistAllSettings(ctx, path, s)
}

func persistAllSettings(ctx context.Context, path string, s settings) error {
	draft := s.TSharkPath
	if draft != "" {
		tool, err := capturepkg.ConfiguredTShark(draft)
		if err != nil {
			return err
		}
		if err = validateTShark(ctx, tool); err != nil {
			return err
		}
	}
	if _, err := s.Port(); err != nil {
		return err
	}
	if s.BootPath != "" {
		if _, err := s.Boot(); err != nil {
			return err
		}
	}
	if s.JavaPath != "" {
		java, err := s.Java()
		if err != nil {
			return err
		}
		checkCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		cmd := exec.CommandContext(checkCtx, java, "-version")
		platformpkg.HideProcess(cmd)
		var out platformpkg.TailWriter
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Run(); err != nil {
			return i18npkg.MessageError("settings.unable_to_run_java_w", err)
		}
		if !strings.Contains(strings.ToLower(string(out.Buf)), "version") {
			return i18npkg.MessageError("settings.the_selected_executable_did_not_return_a")
		}
	}
	if err := writeSettings(path, s); err != nil {
		return err
	}
	saved, err := readSettings(path)
	if err != nil {
		return err
	}
	if saved != s {
		return i18npkg.MessageError("settings.saved_settings_could_not_be_verified_please")
	}
	return nil
}

func (a *app) chooseTShark() {
	if a.settingsBusy {
		return
	}
	a.settingsBusy = true
	parent := a.settingsWindow
	if parent == nil {
		parent = a.win
	}
	dialogTitle, filterName := a.tr("settings.select_tshark_exe"), a.tr("settings.tshark_executable")
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: parent, Title: dialogTitle, Filters: []mygo.FileFilter{{Name: filterName, Extensions: []string{"exe"}}}})
		a.updateSettings(func() {
			a.settingsBusy = false
			if err != nil {
				a.settingsStatus = i18npkg.ErrorMessage(err)
			} else if len(paths) > 0 {
				a.settingsDraft = paths[0]
				a.settingsStatus = ""
			}
		})
	}()
}

func (a *app) saveSettings() {
	if a.settingsBusy {
		return
	}
	language := i18npkg.NormalizeLanguage(a.language)
	themeMode := normalizeThemeMode(a.themeMode)
	draft := platformpkg.CleanTSharkPath(a.settingsDraft)
	config := a.arthasDraft
	config.JavaPath = platformpkg.CleanTSharkPath(config.JavaPath)
	config.BootPath = platformpkg.CleanTSharkPath(config.BootPath)
	config.HTTPPort = strings.TrimSpace(config.HTTPPort)
	a.settingsBusy = true
	a.settingsStatus = i18npkg.MessageText("settings.validating_dependency_settings")
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		path, err := settingsPath()
		if err == nil {
			err = persistAllSettings(a.ctx, path, settings{Language: language, ThemeMode: themeMode, TSharkPath: draft, ArthasConfig: config})
		}
		a.updateSettings(func() {
			a.settingsBusy = false
			if err != nil {
				a.settingsStatus = i18npkg.ErrorMessage(err)
				return
			}
			a.tsharkPath = draft
			a.arthasConfig = config
			a.closeSettings()
			a.update(func() {
				if a.pcap != "" {
					a.status = i18npkg.MessageText("settings.tshark_path_saved_you_can_now_read")
					a.failed = false
				} else {
					a.refresh()
				}
			})
		})
	}()
}

func (a *app) settingsView(c *ui.Context) {
	a.applyTheme(c)
	ui.Column(c).FillWidth().FillHeight().Padding(24).Gap(16).Children(func() {
		ui.Scroll(c.Key("settings-content")).FillWidth().Grow(1).Children(func() {
			ui.Column(c).FillWidth().Gap(14).Children(func() {
				ui.Text(c, a.tr("settings.tshark_location")).FontSize(17).Bold()
				ui.Text(c, a.tr("settings.select_tshark_exe_from_your_wireshark_installation")).TextColor(c.Theme().TextMuted)
				ui.Row(c).FillWidth().Gap(12).Children(func() {
					ui.TextInput(c.Key("tshark-path"), &a.settingsDraft).Grow(1).Placeholder(`C:\Program Files\Wireshark\tshark.exe`).Label(a.tr("settings.tshark_executable_path")).Disabled(a.settingsBusy)
					if ui.Button(c, a.tr("settings.browse")).Disabled(a.settingsBusy).Clicked() {
						a.chooseTShark()
					}
				})
				ui.Text(c, a.tr("settings.custom_paths_override_environment_variables_and_system")).TextColor(c.Theme().TextMuted)
				ui.Text(c, a.tr("settings.live_capture_requires_npcap_setting_the_tshark")).TextColor(c.Theme().TextMuted)
				ui.Box(c).FillWidth().Height(1).Background(c.Theme().Border)
				ui.Text(c, a.tr("settings.java_and_arthas")).FontSize(17).Bold()
				ui.Text(c, a.tr("settings.use_a_jdk_compatible_with_the_target")).TextColor(c.Theme().TextMuted)
				ui.Row(c).FillWidth().Gap(12).Children(func() {
					ui.TextInput(c.Key("java-path"), &a.arthasDraft.JavaPath).Grow(1).Label(a.tr("settings.java_executable_path")).Placeholder(a.tr("settings.jdk_java_exe_optional")).Disabled(a.settingsBusy)
					if ui.Button(c.Key("browse-java"), a.tr("settings.browse_java")).Disabled(a.settingsBusy).Clicked() {
						a.chooseArthasDependency(false)
					}
				})
				ui.Row(c).FillWidth().Gap(12).Children(func() {
					ui.TextInput(c.Key("arthas-boot-path"), &a.arthasDraft.BootPath).Grow(1).Label(a.tr("settings.arthas_bootstrap_jar_path")).Placeholder("arthas-boot.jar").Disabled(a.settingsBusy)
					if ui.Button(c.Key("browse-arthas"), a.tr("settings.browse_arthas")).Disabled(a.settingsBusy).Clicked() {
						a.chooseArthasDependency(true)
					}
				})
				ui.TextInput(c.Key("arthas-http-port"), &a.arthasDraft.HTTPPort).FillWidth().Label(a.tr("settings.arthas_http_port")).Placeholder(a.tr("settings.local_arthas_http_port_default_8563")).Disabled(a.settingsBusy)
				ui.Text(c, a.tr("settings.arthas_is_optional_download_the_official_arthas")).TextColor(c.Theme().TextMuted)
				if a.settingsStatus != "" {
					ui.Text(c, a.tr(a.settingsStatus)).Role(ui.RoleStatus).TextColor(c.Theme().Accent)
				}
			})
		})
		ui.Box(c).FillWidth().Height(1).Background(c.Theme().Border)
		ui.Row(c).FillWidth().Height(40).Gap(12).Children(func() {
			ui.Box(c).Grow(1)
			if ui.Button(c, a.tr("common.cancel")).Disabled(a.settingsBusy).Clicked() {
				a.closeSettings()
			}
			if ui.PrimaryButton(c, a.tr("common.save")).MinWidth(96).Disabled(a.settingsBusy).Clicked() {
				a.saveSettings()
			}
		})
	})
}

func (a *app) chooseArthasDependency(jar bool) {
	if a.settingsBusy {
		return
	}
	a.settingsBusy = true
	parent := a.settingsWindow
	if parent == nil {
		parent = a.win
	}
	label, extension := i18npkg.MessageText("settings.select_jdk_java_exe"), "exe"
	if jar {
		label, extension = i18npkg.MessageText("settings.select_arthas_boot_jar"), "jar"
	}
	label = a.tr(label)
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: parent, Title: label, Filters: []mygo.FileFilter{{Name: label, Extensions: []string{extension}}}})
		a.updateSettings(func() {
			a.settingsBusy = false
			if err != nil {
				a.settingsStatus = i18npkg.ErrorMessage(err)
			} else if len(paths) > 0 {
				if jar {
					a.arthasDraft.BootPath = paths[0]
				} else {
					a.arthasDraft.JavaPath = paths[0]
				}
				a.settingsStatus = ""
			}
		})
	}()
}
