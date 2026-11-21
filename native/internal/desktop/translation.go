package desktop

import (
	i18npkg "webhola/native/internal/i18n"
)

func (a *app) tr(text string) string { return i18npkg.TranslateText(a.language, text) }
