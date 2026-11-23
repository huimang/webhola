package desktop

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // image.DecodeConfig
	_ "image/jpeg" // image.DecodeConfig
	_ "image/png"  // image.DecodeConfig
	"mime"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	capturepkg "webhola/native/internal/capture"
	i18npkg "webhola/native/internal/i18n"
)

// Image bodies show metadata by default, with the picture as an opt-in preview.
const (
	bodyImageInfo = iota
	bodyImagePreview
)

// maxImagePixels bounds decoding of compressed payloads that expand into huge
// bitmaps.
const maxImagePixels = 64 << 20

// rawDisplayLimit keeps the raw text view short: rendering a whole binary hex
// dump or a large body stalls the window.
const rawDisplayLimit = 1024

var errImageTooLarge = errors.New("image is too large to preview")

type bodyPreview struct {
	key, source, contentType, text, note string
	rawText, title                       string
	truncated, pretty                    bool
	formatted                            bool
	editor                               *editor.State
	parent                               *mygo.Window
	labelKey, fullscreenTitleKey         string
	image                                bool
	imageMode                            int
	imageFormat                          string
	imageWidth, imageHeight, imageBytes  int
	imageSource                          ui.ImageSource
	imageErr                             error
	rawBody                              []byte
}

func jsonEditorTheme(base *ui.Theme) *editor.Theme {
	t := editor.DefaultTheme(base)
	t.Background = base.Surface
	if base.Dark {
		t.Tokens[editor.TokenProperty].Color = ui.Hex("#79C0FF")
		t.Tokens[editor.TokenString].Color = ui.Hex("#7EE2A8")
		t.Tokens[editor.TokenNumber].Color = ui.Hex("#F6C177")
		t.Tokens[editor.TokenConstant].Color = ui.Hex("#D2A8FF")
		t.Tokens[editor.TokenKeyword].Color = ui.Hex("#D2A8FF")
		t.Tokens[editor.TokenEscape].Color = base.Accent
		return t
	}
	t.Tokens[editor.TokenProperty].Color = ui.Hex("#1D4ED8")
	t.Tokens[editor.TokenString].Color = ui.Hex("#166534")
	t.Tokens[editor.TokenNumber].Color = ui.Hex("#92400E")
	t.Tokens[editor.TokenConstant].Color = ui.Hex("#7E22CE")
	t.Tokens[editor.TokenKeyword].Color = ui.Hex("#7E22CE")
	t.Tokens[editor.TokenEscape].Color = base.Accent
	return t
}

func formatBody(m *capturepkg.Message) (string, bool, string) {
	if m.Body == "" {
		return i18npkg.MessageText("body_view.no_body_captured_or_this_message_has"), false, ""
	}
	media, _, err := mime.ParseMediaType(m.ContentType)
	media = strings.ToLower(media)
	if err != nil || (media != "application/json" && !(strings.HasPrefix(media, "application/") && strings.HasSuffix(media, "+json"))) {
		return m.Body, false, ""
	}
	if m.Truncated {
		return m.Body, false, i18npkg.MessageText("body_view.json_body_is_truncated_showing_raw_text")
	}
	// Bound indentation expansion for unusually deep JSON before allocating it.
	depth := 0
	quoted, escape := false, false
	for _, r := range m.Body {
		if quoted {
			if escape {
				escape = false
			} else if r == '\\' {
				escape = true
			} else if r == '"' {
				quoted = false
			}
			continue
		}
		switch r {
		case '"':
			quoted = true
		case '{', '[':
			depth++
			if depth > 128 {
				return m.Body, false, i18npkg.MessageText("body_view.json_nesting_is_too_deep_showing_raw")
			}
		case '}', ']':
			depth--
		}
	}
	var b bytes.Buffer
	if err := json.Indent(&b, []byte(m.Body), "", "  "); err != nil {
		return m.Body, false, i18npkg.MessageText("body_view.unable_to_parse_json_showing_raw_text")
	}
	return b.String(), true, "JSON · Pretty"
}

func newBodyPreview(key string, m *capturepkg.Message) *bodyPreview {
	if media, isImage := imageMediaType(m.ContentType); isImage && len(m.RawBody) > 0 {
		return newImagePreview(key, m, media)
	}
	text, pretty, note := formatBody(m)
	raw := m.Body
	if raw == "" {
		raw = i18npkg.MessageText("body_view.no_body_captured_or_this_message_has")
	}
	p := &bodyPreview{key: key, source: m.Body, contentType: m.ContentType, truncated: m.Truncated, text: text, rawText: raw, pretty: pretty, note: note, title: m.Line}
	if pretty {
		p.editor = &editor.State{Buffer: editor.NewBuffer(text), ReadOnly: true, SoftWrap: true, NoScrollPastEnd: true, NoActiveLine: true, Label: i18npkg.MessageText("body_view.http_body"), Theme: jsonEditorTheme(brandTheme(false))}
		p.editor.Buffer.SetLanguage(editor.LanguageNamed("json"))
	}
	return p
}

func imageMediaType(contentType string) (string, bool) {
	media, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", false
	}
	media = strings.ToLower(media)
	return media, strings.HasPrefix(media, "image/")
}

func newImagePreview(key string, m *capturepkg.Message, media string) *bodyPreview {
	raw := m.Body
	if raw == "" {
		raw = i18npkg.MessageText("body_view.no_body_captured_or_this_message_has")
	}
	size := m.Bytes
	if size == 0 {
		size = len(m.RawBody)
	}
	p := &bodyPreview{key: key, source: m.Body, contentType: m.ContentType, truncated: m.Truncated, text: m.Body, rawText: raw, title: m.Line, image: true, imageBytes: size, rawBody: m.RawBody}
	source, width, height, format, err := decodePreviewImage(m.RawBody)
	p.imageSource, p.imageWidth, p.imageHeight, p.imageErr = source, width, height, err
	if format == "" {
		format = imageFormatFromMedia(media)
	}
	p.imageFormat = format
	return p
}

func imageFormatFromMedia(media string) string {
	name := strings.TrimPrefix(media, "image/")
	if i := strings.IndexByte(name, '+'); i > 0 {
		name = name[:i]
	}
	return strings.ToUpper(name)
}

// decodePreviewImage reads the header first so oversized images never expand
// into a huge bitmap.
func decodePreviewImage(raw []byte) (ui.ImageSource, int, int, string, error) {
	format := ""
	if config, name, err := image.DecodeConfig(bytes.NewReader(raw)); err == nil {
		format = strings.ToUpper(name)
		if int64(config.Width)*int64(config.Height) > maxImagePixels {
			return nil, config.Width, config.Height, format, errImageTooLarge
		}
	}
	source, err := ui.DecodeImage(raw)
	if err != nil {
		return nil, 0, 0, format, err
	}
	width, height := ui.ImageSize(source)
	return source, int(width), int(height), format, nil
}

func formatByteSize(bytes int) string {
	switch {
	case bytes < 1024:
		return fmt.Sprintf("%d B", bytes)
	case bytes < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
	}
}

func (a *app) bodyView(c *ui.Context, key string, m *capturepkg.Message) {
	p := a.bodyPreview
	if p == nil || p.key != key || p.source != m.Body || p.contentType != m.ContentType || p.truncated != m.Truncated {
		p = newBodyPreview(key, m)
		a.bodyPreview = p
	}
	a.bodyToolbar(c, p, false)
	a.renderBody(c, p)
}

func (a *app) bodyToolbar(c *ui.Context, p *bodyPreview, fullscreen bool) {
	ui.Row(c).FillWidth().Gap(8).Children(func() {
		if p.image {
			if ui.Button(c, a.tr("body_view.preview")).Selected(p.imageMode == bodyImagePreview).Disabled(p.imageSource == nil).Clicked() {
				p.imageMode = bodyImagePreview
			}
		} else {
			if ui.Button(c, a.tr("body_view.raw")).Selected(!p.formatted).Clicked() {
				p.formatted = false
			}
			if ui.Button(c, a.tr("body_view.format")).Selected(p.formatted).Disabled(!p.pretty).Clicked() {
				p.formatted = true
			}
		}
		ui.Box(c).Grow(1)
		if fullscreen {
			if ui.Button(c, a.tr("body_view.exit_full_screen")).Clicked() {
				a.closeBodyFullscreen()
			}
		} else {
			if ui.Button(c, a.tr("body_view.full_screen")).Disabled(p.source == "").Clicked() {
				a.openBodyFullscreen(p)
			}
		}
	})
}

func (a *app) renderBody(c *ui.Context, p *bodyPreview) {
	if p.truncated {
		ui.Text(c, a.tr("capture.body_preview_truncated_64_kib")).FontSize(12).TextColor(c.Theme().Warning)
	}
	if p.image {
		a.renderImageBody(c, p)
		return
	}
	if p.note != "" && (p.formatted || !p.pretty) {
		color := c.Theme().TextMuted
		if !p.pretty {
			color = c.Theme().Warning
		}
		ui.Text(c, a.tr(p.note)).FontSize(12).TextColor(color)
	}
	if p.pretty && p.formatted {
		p.editor.Theme = jsonEditorTheme(c.Theme())
		p.editor.Label = a.tr(p.viewerLabel())
		editor.View(c.Key(p.key+"-pretty"), p.editor).FillWidth().Grow(1).MinHeight(100).Border(1, c.Theme().Border).Radius(8)
	} else {
		a.renderRawText(c, p)
	}
}

func (a *app) renderRawText(c *ui.Context, p *bodyPreview) {
	raw := p.rawText
	if p.source == "" {
		raw = a.tr(raw)
	}
	shown, cut := truncateText(raw, rawDisplayLimit)
	if cut {
		ui.Text(c, a.tr("body_view.raw_text_is_limited_to_the_first_1_kib")).FontSize(12).TextColor(c.Theme().TextMuted)
	}
	ui.TextArea(c.Key(p.key+"-raw"), &shown).ReadOnly(true).FillWidth().Grow(1).MinHeight(100).Font("Consolas").FontSize(13).Label(a.tr(p.viewerLabel()))
}

// truncateText cuts text to at most limit bytes without splitting a rune.
func truncateText(text string, limit int) (string, bool) {
	if len(text) <= limit {
		return text, false
	}
	cut := limit
	for cut > 0 && text[cut]&0xC0 == 0x80 {
		cut--
	}
	return text[:cut], true
}

func (a *app) renderImageBody(c *ui.Context, p *bodyPreview) {
	ui.Column(c).FillWidth().Grow(1).Gap(8).Children(func() {
		ui.Column(c).FillWidth().Gap(2).Children(func() {
			if p.imageFormat != "" {
				ui.Textf(c, a.tr("body_view.image_format"), p.imageFormat).FontSize(13).Selectable()
			}
			if p.imageWidth > 0 && p.imageHeight > 0 {
				ui.Textf(c, a.tr("body_view.image_dimensions"), p.imageWidth, p.imageHeight).FontSize(13).Selectable()
				ui.Textf(c, a.tr("body_view.image_resolution"), float64(p.imageWidth)*float64(p.imageHeight)/1e6).FontSize(13).Selectable()
			}
			ui.Textf(c, a.tr("body_view.image_size"), formatByteSize(p.imageBytes)).FontSize(13).Selectable()
			if p.imageErr != nil {
				note := "body_view.image_preview_unavailable"
				if errors.Is(p.imageErr, errImageTooLarge) {
					note = "body_view.image_too_large_to_preview"
				}
				ui.Text(c, a.tr(note)).FontSize(12).TextColor(c.Theme().Warning)
			}
		})
		if p.imageMode == bodyImagePreview && p.imageSource != nil {
			ui.Image(c, p.imageSource).FillWidth().Grow(1).Fit(ui.Contain).Label(a.tr("body_view.image_preview"))
		}
	})
}

func (p *bodyPreview) viewerLabel() string {
	if p.labelKey != "" {
		return p.labelKey
	}
	return "body_view.http_body"
}

func (a *app) bodyWindowTitle() string {
	if a.bodyFullscreen != nil && a.bodyFullscreen.fullscreenTitleKey != "" {
		return a.bodyFullscreen.fullscreenTitleKey
	}
	return "body_view.webhola_full_screen_body"
}

func (a *app) openBodyFullscreen(p *bodyPreview) {
	if a.bodyWindow != nil {
		a.bodyWindow.Focus()
		return
	}
	// Each native window owns its editor state; keep the selected message as a snapshot.
	copy := newBodyPreview(p.key, &capturepkg.Message{Line: p.title, Body: p.source, RawBody: p.rawBody, ContentType: p.contentType, Truncated: p.truncated, Bytes: p.imageBytes})
	copy.formatted = p.formatted
	copy.imageMode = p.imageMode
	copy.parent = p.parent
	copy.labelKey = p.labelKey
	copy.fullscreenTitleKey = p.fullscreenTitleKey
	a.bodyFullscreen = copy
	if a.win == nil {
		return
	}
	parent := p.parent
	if parent == nil {
		parent = a.win
	}
	w := mygo.NewWindow(mygo.WindowOptions{Title: a.tr(a.bodyWindowTitle()), Width: 1200, Height: 820, MinWidth: 640, MinHeight: 400, Parent: parent, Modal: true, FullScreen: true, Content: ui.View(a.bodyFullscreenView)})
	a.bodyWindow = w
	_ = w.SetIcon(windowIcon)
	w.OnClosed(func() {
		a.bodyWindow = nil
		a.bodyFullscreen = nil
		a.win.Invalidate()
		if parent != a.win {
			parent.Invalidate()
		}
	})
	w.Show()
	w.SetFullScreen(true)
}

func (a *app) closeBodyFullscreen() {
	if a.bodyWindow != nil {
		a.bodyWindow.Close()
	} else {
		a.bodyFullscreen = nil
	}
}

func (a *app) bodyFullscreenView(c *ui.Context) {
	a.applyTheme(c)
	if c.Shortcut(0, ui.KeyEscape) {
		a.closeBodyFullscreen()
		return
	}
	p := a.bodyFullscreen
	if p == nil {
		return
	}
	ui.Column(c).FillWidth().FillHeight().Padding(20).Gap(12).Children(func() {
		title := p.title
		if p.labelKey != "" {
			title = a.tr(title)
		}
		ui.Text(c, "Body · "+title).FontSize(16).Bold().Selectable()
		ui.Text(c, p.contentType+a.tr("body_view.esc_to_exit_full_screen")).FontSize(12).TextColor(c.Theme().TextMuted)
		a.bodyToolbar(c, p, true)
		a.renderBody(c, p)
	})
}
