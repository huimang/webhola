package desktop

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	i18npkg "webhola/native/internal/i18n"
	jvmpkg "webhola/native/internal/jvm"
)

type javaSourceViewer struct {
	owner           *arthasState
	requestedLoader string
	ctx             context.Context
	cancel          context.CancelFunc
	client          *jvmpkg.ArthasClient
	method          jvmpkg.JavaMethod
	loaders         []string
	loading         bool
	status          string
	editor          *editor.State
	line            int
	document        jvmpkg.JavaSourceDocument
	highlightRows   []int
}

var sourceCodeIcon = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8Z"/><path d="M14 2v6h6M10 12l-3 3 3 3m4-6 3 3-3 3"/></svg>`))

func (a *app) javaSourceIcon(c *ui.Context, s *arthasState, method jvmpkg.JavaMethod, key string, line int) {
	label := a.tr(i18npkg.MessageText("source.view_class", method.ClassName+"#"+method.MethodName))
	if ui.ButtonBase(c.Key(key)).Size(24, 24).Padding(3).Radius(4).Shrink(0).Label(label).Tooltip(label).Cursor(ui.CursorPointer).
		Disabled(s.connection() == nil || !jvmpkg.JavaClassName.MatchString(method.ClassName)).Children(func() {
		ui.Icon(c, sourceCodeIcon).Size(18, 18).TextColor(c.Theme().TextMuted)
	}).Clicked() {
		a.openJavaSource(s, method, line)
	}
}

func sourceEditor(source string) *editor.State {
	state := &editor.State{Buffer: editor.NewBuffer(source), ReadOnly: true, NoScrollPastEnd: true, NoActiveLine: true}
	state.Buffer.SetLanguage(editor.LanguageNamed("java"))
	return state
}

func (a *app) openJavaSource(s *arthasState, method jvmpkg.JavaMethod, lines ...int) {
	line := 0
	if len(lines) > 0 {
		line = lines[0]
	}
	if !jvmpkg.JavaClassName.MatchString(method.ClassName) || s.ctx.Err() != nil {
		return
	}
	for i, existing := range a.javaSources {
		if existing.owner == s && existing.method.ClassName == method.ClassName && existing.requestedLoader == method.Loader {
			a.javaSource = existing
			existing.line = line
			a.positionJavaSource(existing)
			a.workspace = a.sourceTabStart() + i
			return
		}
	}
	ctx, cancel := context.WithCancel(s.ctx)
	v := &javaSourceViewer{owner: s, requestedLoader: method.Loader, ctx: ctx, cancel: cancel, client: s.connection(), method: method, line: line}
	a.javaSources = append(a.javaSources, v)
	a.javaSource = v
	a.workspace = a.sourceTabStart() + len(a.javaSources) - 1
	a.loadJavaSource(s, v, true)
}

func (a *app) loadJavaSource(s *arthasState, v *javaSourceViewer, discover bool) {
	if v.loading || v.ctx.Err() != nil {
		return
	}
	if v.client == nil {
		v.status = i18npkg.MessageText("source.connect_first")
		return
	}
	v.loading, v.status, v.editor = true, i18npkg.MessageText("source.loading"), nil
	loader := v.method.Loader
	s.jobs.Add(1)
	a.workers.Add(1)
	go func() {
		defer s.jobs.Done()
		defer a.workers.Done()
		ctx, cancel := context.WithTimeout(v.ctx, 15*time.Second)
		defer cancel()
		loaders := v.loaders
		var err error
		if discover {
			loaders, err = v.client.SourceLoaders(ctx, v.method.ClassName)
			found := false
			for _, hash := range loaders {
				if hash == loader {
					found = true
				}
			}
			if !found {
				loader = ""
				if len(loaders) == 1 {
					loader = loaders[0]
				}
			}
		}
		document := jvmpkg.JavaSourceDocument{}
		if err == nil && loader != "" {
			document, err = v.client.ClassSourceDocument(ctx, v.method.ClassName, loader)
		}
		a.update(func() {
			if !a.hasJavaSource(v) {
				return
			}
			v.loading = false
			v.loaders, v.method.Loader = loaders, loader
			switch {
			case err != nil:
				v.status = i18npkg.ErrorMessage(err)
			case loader == "":
				v.status = i18npkg.MessageText("source.choose_loader")
			default:
				v.status = ""
				v.document = document
				v.editor = sourceEditor(document.Text)
				a.positionJavaSource(v)
			}
		})
	}()
}

func (a *app) sourceTabStart() int {
	if a.arthas != nil {
		return 2
	}
	return 1
}

func (a *app) selectedJavaSource() *javaSourceViewer {
	index := a.workspace - a.sourceTabStart()
	if index >= 0 && index < len(a.javaSources) {
		return a.javaSources[index]
	}
	return nil
}

func (a *app) hasJavaSource(v *javaSourceViewer) bool {
	for _, source := range a.javaSources {
		if source == v {
			return true
		}
	}
	return false
}

func (v *javaSourceViewer) tabLabel() string {
	name := v.method.ClassName
	if dot := strings.LastIndex(name, "."); dot >= 0 {
		name = name[dot+1:]
	}
	return name + ".java"
}

func (a *app) closeJavaSource(v *javaSourceViewer) {
	for i, source := range a.javaSources {
		if source != v {
			continue
		}
		v.cancel()
		a.javaSources = append(a.javaSources[:i], a.javaSources[i+1:]...)
		if a.javaSource == v {
			a.javaSource = nil
		}
		a.workspace = 0
		if a.arthas != nil {
			a.workspace = 1
		}
		return
	}
}

func (a *app) javaSourceView(c *ui.Context, v *javaSourceViewer) {
	a.applyTheme(c)
	if c.Shortcut(0, ui.KeyEscape) {
		a.closeJavaSource(v)
		return
	}
	ui.Column(c).FillWidth().FillHeight().Padding(20).Gap(12).Children(func() {
		ui.Text(c, v.method.ClassName).FontSize(18).Bold().Selectable()
		ui.Text(c, a.tr("source.decompiled_hint")).FontSize(12).TextColor(c.Theme().TextMuted)
		if len(v.loaders) > 1 {
			ui.Row(c).FillWidth().Gap(8).Children(func() {
				ui.Text(c, a.tr("arthas_monitor.classloader"))
				if ui.Select(c.Key("source-loader"), &v.method.Loader, v.loaders).Width(180).Label(a.tr("source.loader_choice")).Disabled(v.loading).Changed() {
					if v.owner != nil {
						a.loadJavaSource(v.owner, v, false)
					}
				}
			})
		}
		if v.loading {
			ui.Spinner(c).Label(a.tr("source.loading"))
		}
		if v.status != "" {
			ui.Text(c, a.tr(v.status)).Role(ui.RoleStatus).TextColor(c.Theme().TextMuted)
		}
		if v.editor != nil {
			v.editor.Theme = editor.DefaultTheme(c.Theme())
			v.editor.Theme.Background = c.Theme().Surface
			v.editor.LineBackgrounds = nil
			for _, row := range v.highlightRows {
				v.editor.LineBackgrounds = append(v.editor.LineBackgrounds, editor.LineBackground{First: row, Last: row, Color: c.Theme().Selection})
			}
			v.editor.Label = a.tr("source.code")
			editor.View(c.Key("java-source-code"), v.editor).FillWidth().Grow(1).MinHeight(100).Border(1, c.Theme().Border).Radius(6)
		} else {
			ui.Box(c).Grow(1)
		}
	})
}

func (a *app) positionJavaSource(v *javaSourceViewer) {
	if v.editor == nil {
		return
	}
	v.status = ""
	v.highlightRows = append([]int(nil), v.document.Lines[v.line]...)
	sort.Ints(v.highlightRows)
	if len(v.highlightRows) > 0 {
		v.editor.GoTo(editor.Pos{Line: v.highlightRows[0], Col: 0})
		v.status = i18npkg.MessageText("source.positioned_line", v.line)
	} else if v.line > 0 {
		v.status = i18npkg.MessageText("source.line_unavailable", v.line)
	}
}

func traceSourceLocation(node jvmpkg.TraceMethod, root jvmpkg.JavaMethod) (jvmpkg.JavaMethod, int) {
	target := node.JavaMethod
	line := 0
	if node.Line > 0 && node.Caller.ClassName != "" {
		target = node.Caller
		line = node.Line
	}
	if target.ClassName == root.ClassName {
		target.Loader = root.Loader
	}
	return target, line
}
