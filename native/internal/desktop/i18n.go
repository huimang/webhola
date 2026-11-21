package desktop

import (
	"github.com/egoist/mygo"

	i18npkg "webhola/native/internal/i18n"
)

// Change only the language field, preserving dependency settings without
// requiring the optional executables to be installed or revalidated.
func saveLanguage(path, language string) error {
	s, err := readSettings(path)
	if err != nil {
		return err
	}
	s.Language = i18npkg.NormalizeLanguage(language)
	return writeSettings(path, s)
}

func (a *app) changeLanguage(language string) {
	if a.win != nil {
		defer a.installMenu()
	}
	language = i18npkg.NormalizeLanguage(language)
	if i18npkg.NormalizeLanguage(a.language) == language {
		return
	}
	path, err := settingsPath()
	if err == nil {
		err = saveLanguage(path, language)
	}
	if err != nil {
		a.status = i18npkg.MessageText("i18n.unable_to_save_language_preference") + i18npkg.ErrorMessage(err)
		a.failed = true
		return
	}
	a.language = language
	if a.win != nil {
		a.win.Invalidate()
	}
	for _, window := range []struct {
		window *mygo.Window
		title  string
	}{
		{a.settingsWindow, i18npkg.MessageText("i18n.webhola_dependency_settings")},
		{a.aboutWindow, i18npkg.MessageText("about.about_webhola")},
		{a.bodyWindow, a.bodyWindowTitle()},
	} {
		if window.window != nil {
			window.window.SetTitle(a.tr(window.title))
			window.window.Invalidate()
		}
	}
	for _, p := range []*bodyPreview{a.bodyPreview, a.bodyFullscreen} {
		if p != nil && p.editor != nil {
			p.editor.Label = a.tr(p.viewerLabel())
		}
	}
}

func (a *app) languageMenuItems() []*mygo.MenuItem {
	return []*mygo.MenuItem{
		{Label: a.tr("language.chinese"), Type: mygo.MenuItemRadio, Checked: i18npkg.NormalizeLanguage(a.language) == "zh-CN", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.changeLanguage("zh-CN") }},
		{Label: a.tr("language.english"), Type: mygo.MenuItemRadio, Checked: i18npkg.NormalizeLanguage(a.language) == "en", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.changeLanguage("en") }},
	}
}
