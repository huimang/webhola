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
	"unicode"
	"unicode/utf8"

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

// bodyMode selects how a captured message is presented.
const (
	bodyModeDefault = iota
	bodyModeSQL
	bodyModeResult
	bodyModeBinary
	bodyModeCommand
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
	rawText, title, headers              string
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
	mode                                 int
	table                                *ui.ListState
	tableSel                             ui.Selection[int]
	tableCols                            []ui.TableColumn
	tableRows                            [][]string
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

func newBodyPreview(key string, m *capturepkg.Message, mode int) *bodyPreview {
	if media, isImage := imageMediaType(m.ContentType); isImage && len(m.RawBody) > 0 {
		return newImagePreview(key, m, media)
	}
	if mode == bodyModeBinary {
		// Raw keeps the binary hex dump; the format view shows only readable
		// strings: the prepared statement SQL, or the text found in the payload.
		p := &bodyPreview{key: key, source: m.Body, headers: m.Headers, contentType: m.ContentType, truncated: m.Truncated, text: m.Body, rawText: m.Body, title: m.Line, mode: bodyModeBinary, pretty: true}
		if sql, ok := strings.CutPrefix(m.Headers, "Statement: "); ok && sql != "" {
			p.editor = &editor.State{Buffer: editor.NewBuffer(sql), ReadOnly: true, SoftWrap: true, NoScrollPastEnd: true, NoActiveLine: true, Label: i18npkg.MessageText("body_view.sql_body"), Theme: jsonEditorTheme(brandTheme(false))}
			p.editor.Buffer.SetLanguage(mysqlSQLLanguage())
		} else {
			text := printableStrings(m.RawBody, 8192)
			if text == "" {
				text = i18npkg.MessageText("body_view.no_readable_text")
			}
			p.editor = &editor.State{Buffer: editor.NewBuffer(text), ReadOnly: true, SoftWrap: true, NoScrollPastEnd: true, NoActiveLine: true, Label: i18npkg.MessageText("body_view.readable_text"), Theme: jsonEditorTheme(brandTheme(false))}
			p.editor.Buffer.SetLanguage(mysqlSQLLanguage())
		}
		return p
	}
	if mode == bodyModeCommand {
		text := m.Body
		if text == "" {
			text = i18npkg.MessageText("body_view.no_body_captured_or_this_message_has")
		}
		p := &bodyPreview{key: key, source: m.Body, headers: m.Headers, contentType: m.ContentType, truncated: m.Truncated, text: text, rawText: text, pretty: true, title: m.Line, mode: bodyModeCommand}
		p.editor = &editor.State{Buffer: editor.NewBuffer(text), ReadOnly: true, SoftWrap: true, NoScrollPastEnd: true, NoActiveLine: true, Label: i18npkg.MessageText("body_view.body"), Theme: jsonEditorTheme(brandTheme(false))}
		return p
	}
	if mode == bodyModeSQL && m.Body != "" {
		p := &bodyPreview{key: key, source: m.Body, headers: m.Headers, contentType: m.ContentType, truncated: m.Truncated, text: m.Body, rawText: m.Body, pretty: true, formatted: true, title: m.Line, mode: bodyModeSQL}
		p.editor = &editor.State{Buffer: editor.NewBuffer(m.Body), ReadOnly: true, SoftWrap: true, NoScrollPastEnd: true, NoActiveLine: true, Label: i18npkg.MessageText("body_view.sql_body"), Theme: jsonEditorTheme(brandTheme(false))}
		p.editor.Buffer.SetLanguage(mysqlSQLLanguage())
		return p
	}
	if mode == bodyModeResult {
		if header, rows, ok := parseMySQLRows(m.Body); ok && len(header) == mysqlColumnCount(m.Headers) {
			p := &bodyPreview{key: key, source: m.Body, headers: m.Headers, contentType: m.ContentType, truncated: m.Truncated, text: m.Body, rawText: m.Body, pretty: true, formatted: true, title: m.Line, mode: bodyModeResult, tableRows: rows}
			// Cell selection carries the framework's row striping.
			p.table = &ui.ListState{Selection: &p.tableSel, CellSelect: true}
			p.tableCols = make([]ui.TableColumn, len(header))
			for i, name := range header {
				p.tableCols[i] = ui.TableColumn{Title: name, ID: fmt.Sprintf("c%d", i), MinWidth: 60}
			}
			return p
		}
	}
	text, pretty, note := formatBody(m)
	raw := m.Body
	if raw == "" {
		raw = i18npkg.MessageText("body_view.no_body_captured_or_this_message_has")
	}
	p := &bodyPreview{key: key, source: m.Body, headers: m.Headers, contentType: m.ContentType, truncated: m.Truncated, text: text, rawText: raw, pretty: pretty, note: note, title: m.Line}
	if pretty {
		p.editor = &editor.State{Buffer: editor.NewBuffer(text), ReadOnly: true, SoftWrap: true, NoScrollPastEnd: true, NoActiveLine: true, Label: i18npkg.MessageText("body_view.http_body"), Theme: jsonEditorTheme(brandTheme(false))}
		p.editor.Buffer.SetLanguage(editor.LanguageNamed("json"))
	}
	return p
}

// mysqlKeywords are MySQL statements and clauses the editor's SQL language
// does not know, so highlighting covers them too.
var mysqlKeywords = map[string]bool{
	"show": true, "tables": true, "databases": true, "columns": true, "status": true,
	"variables": true, "processlist": true, "use": true, "describe": true, "kill": true,
	"lock": true, "unlock": true, "call": true, "declare": true, "cursor": true,
	"handler": true, "loop": true, "repeat": true, "while": true, "iterate": true,
	"leave": true, "signal": true, "resignal": true, "savepoint": true, "release": true,
	"delimiter": true, "engine": true, "auto_increment": true, "unsigned": true,
	"zerofill": true, "charset": true, "collate": true, "partition": true,
	"ignore": true, "low_priority": true, "high_priority": true, "straight_join": true,
	"outfile": true, "dumpfile": true, "duplicate": true, "value": true, "rows": true,
	"found_rows": true, "last_insert_id": true, "interval": true, "sql_no_cache": true,
	"sql_cache": true,
}

// mysqlSQLLanguage extends the SQL language with the MySQL keywords above.
func mysqlSQLLanguage() *editor.Language {
	base := editor.LanguageNamed("sql")
	if base == nil {
		return nil
	}
	lang := *base
	inner := base.Tokenizer
	lang.Tokenizer = editor.TokenizerFunc(func(line string, state uint64, tokens []editor.Token) ([]editor.Token, uint64) {
		first := len(tokens)
		tokens, next := inner.Tokenize(line, state, tokens)
		start := 0
		for i := first; i < len(tokens); i++ {
			end := tokens[i].End
			if end < start || end > len(line) {
				start = end
				continue
			}
			switch tokens[i].Kind {
			case editor.TokenString, editor.TokenComment, editor.TokenNumber:
			default:
				if mysqlKeywords[strings.ToLower(line[start:end])] {
					tokens[i].Kind = editor.TokenKeyword
				}
			}
			start = end
		}
		return tokens, next
	})
	return &lang
}

// printableStrings extracts runs of readable text from a binary payload, so
// the format view can show string values without the hex dump.
func printableStrings(raw []byte, limit int) string {
	var out, run []rune
	flush := func() {
		if len(run) >= 2 && len(out) < limit {
			out = append(out, run...)
			out = append(out, '\n')
		}
		run = run[:0]
	}
	for len(raw) > 0 && len(out) < limit {
		r, size := utf8.DecodeRune(raw)
		if r == utf8.RuneError && size == 1 {
			flush()
			raw = raw[1:]
			continue
		}
		if unicode.IsPrint(r) {
			run = append(run, r)
		} else {
			flush()
		}
		raw = raw[size:]
	}
	flush()
	return string(out)
}

// mysqlColumnCount reads the column count from the captured result headers.
func mysqlColumnCount(headers string) int {
	names, ok := strings.CutPrefix(headers, "Columns: ")
	if !ok || names == "" {
		return 0
	}
	return len(strings.Split(names, ", "))
}

// parseMySQLRows splits the tab-separated result preview into a header and
// rows. Empty cells stay empty; the preview already turned embedded tabs and
// newlines into spaces.
func parseMySQLRows(body string) ([]string, [][]string, bool) {
	body = strings.TrimRight(body, "\n")
	if body == "" {
		return nil, nil, false
	}
	lines := strings.Split(body, "\n")
	header := strings.Split(lines[0], "\t")
	rows := make([][]string, 0, len(lines)-1)
	for _, line := range lines[1:] {
		cells := strings.Split(line, "\t")
		if len(cells) > len(header) {
			cells = cells[:len(header)]
		}
		rows = append(rows, cells)
	}
	return header, rows, true
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

func (a *app) bodyView(c *ui.Context, key string, m *capturepkg.Message, mode int) {
	p := a.bodyPreview
	if p == nil || p.key != key || p.source != m.Body || p.contentType != m.ContentType || p.truncated != m.Truncated || p.mode != mode || p.headers != m.Headers {
		p = newBodyPreview(key, m, mode)
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
			formatLabel := "body_view.format"
			if p.mode == bodyModeResult {
				formatLabel = "body_view.table"
			}
			if ui.Button(c, a.tr(formatLabel)).Selected(p.formatted).Disabled(!p.pretty).Clicked() {
				p.formatted = true
			}
		}
		ui.Box(c).Grow(1)
		if fullscreen {
			if ui.Button(c, a.tr("body_view.exit_full_screen")).Clicked() {
				a.closeBodyFullscreen()
			}
		} else {
			if ui.Button(c, a.tr("body_view.full_screen")).Disabled(p.source == "" && p.editor == nil).Clicked() {
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
	if p.mode == bodyModeResult {
		if p.formatted {
			a.renderResultTable(c, p)
		} else {
			a.renderRawText(c, p)
		}
		return
	}
	if p.pretty && p.formatted {
		p.editor.Theme = jsonEditorTheme(c.Theme())
		p.editor.Label = a.tr(p.viewerLabel())
		editor.View(c.Key(p.key+"-pretty"), p.editor).FillWidth().Grow(1).MinHeight(100).Border(1, c.Theme().Border).Radius(8)
	} else {
		a.renderRawText(c, p)
	}
}

func (a *app) renderResultTable(c *ui.Context, p *bodyPreview) {
	// Zebra rows: every other row gets a faint tint. Chosen cells keep the
	// table's own colors, so striping adds no visible selection effect.
	theme := c.Theme()
	p.table.CellsStyle = ui.CellsStyle{Cell: theme.Surface, CellText: theme.Text, Stripe: theme.Text.Alpha(0.04)}
	rows := p.tableRows
	ui.Table(c.Key(p.key+"-table"), p.table, p.tableCols, len(rows), func(row, col int) {
		if row < len(rows) && col < len(rows[row]) {
			ui.Text(c, rows[row][col]).SingleLine()
		}
	}).FillWidth().Grow(1).MinHeight(100).Border(1, c.Theme().Border).Radius(8)
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
	switch p.mode {
	case bodyModeSQL:
		return "body_view.sql_body"
	case bodyModeResult:
		return "body_view.result_set"
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
	copy := newBodyPreview(p.key, &capturepkg.Message{Line: p.title, Body: p.source, Headers: p.headers, RawBody: p.rawBody, ContentType: p.contentType, Truncated: p.truncated, Bytes: p.imageBytes}, p.mode)
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
