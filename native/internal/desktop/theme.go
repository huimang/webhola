package desktop

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	i18npkg "webhola/native/internal/i18n"
)

func normalizeThemeMode(mode string) string {
	switch mode {
	case "light", "dark":
		return mode
	default:
		return "system"
	}
}

func themeIsDark(mode string, systemDark bool) bool {
	switch normalizeThemeMode(mode) {
	case "light":
		return false
	case "dark":
		return true
	default:
		return systemDark
	}
}

func brandTheme(dark bool) *ui.Theme {
	t := ui.LightTheme()
	t.Background = ui.Hex("#F5F8FA")
	t.Surface = ui.Hex("#FFFFFF")
	t.Text = ui.Hex("#172D46")
	t.TextMuted = ui.Hex("#5C6E82")
	t.Border = ui.Hex("#D9E2EA")
	t.Accent = ui.Hex("#087F8C")
	t.AccentHover = ui.Hex("#076B76")
	t.AccentPressed = ui.Hex("#065B64")
	t.AccentText = ui.Hex("#FFFFFF")
	t.Selection = ui.Hex("#E6F7F7")
	t.Danger = ui.Hex("#B91C1C")
	t.Warning = ui.Hex("#B45309")
	t.Success = ui.Hex("#15803D")
	if dark {
		t = ui.DarkTheme()
		t.Background = ui.Hex("#101923")
		t.Surface = ui.Hex("#172431")
		t.SurfaceHover = ui.Hex("#223544")
		t.SurfacePressed = ui.Hex("#2B4254")
		t.Text = ui.Hex("#E5EDF5")
		t.TextMuted = ui.Hex("#A4B5C6")
		t.Border = ui.Hex("#354A5C")
		t.Accent = ui.Hex("#4DD4CF")
		t.AccentHover = ui.Hex("#75E3DE")
		t.AccentPressed = ui.Hex("#32BBB7")
		t.AccentText = ui.Hex("#102329")
		t.Selection = ui.Hex("#21474F")
		t.Danger = ui.Hex("#FF9696")
		t.Warning = ui.Hex("#F6C177")
		t.Success = ui.Hex("#7EE2A8")
	}
	t.Focus = t.Accent
	t.FocusRingWidth = 2
	return t
}

func (a *app) applyTheme(c *ui.Context) {
	// MyGO supplies the current platform appearance at the start of every
	// frame and redraws native views when that appearance changes.
	c.SetTheme(brandTheme(themeIsDark(a.themeMode, c.Theme().Dark)))
}

func saveThemeMode(path, mode string) error {
	s, err := readSettings(path)
	if err != nil {
		return err
	}
	s.ThemeMode = normalizeThemeMode(mode)
	return writeSettings(path, s)
}

func (a *app) changeThemeMode(mode string) {
	// Native radio items toggle before their callback. Rebuild from the saved
	// application state so a failed write also restores the previous checkmark.
	if a.win != nil {
		defer a.installMenu()
	}
	mode = normalizeThemeMode(mode)
	if mode == normalizeThemeMode(a.themeMode) {
		return
	}
	path, err := settingsPath()
	if err == nil {
		err = saveThemeMode(path, mode)
	}
	if err != nil {
		a.status = i18npkg.MessageText("theme.unable_to_save_theme_preference") + i18npkg.ErrorMessage(err)
		a.failed = true
		return
	}
	a.themeMode = mode
	mygo.Theme.SetSource(mygo.ThemeSource(mode))
	for _, w := range []*mygo.Window{a.win, a.settingsWindow, a.aboutWindow, a.bodyWindow} {
		if w != nil {
			w.Invalidate()
		}
	}
}

func (a *app) themeMenuItems() []*mygo.MenuItem {
	modes := []string{"system", "light", "dark"}
	labels := []string{a.tr("theme.system"), a.tr("theme.light"), a.tr("theme.dark")}
	items := make([]*mygo.MenuItem, 0, len(modes))
	for i, mode := range modes {
		items = append(items, &mygo.MenuItem{
			Label: labels[i], Type: mygo.MenuItemRadio,
			Checked: normalizeThemeMode(a.themeMode) == mode,
			Click:   func(_ *mygo.MenuItem, _ *mygo.Window) { a.changeThemeMode(mode) },
		})
	}
	return items
}
