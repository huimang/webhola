package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	i18npkg "webhola/native/internal/i18n"
	jvmpkg "webhola/native/internal/jvm"
)

func TestSourceTabsRetainIndependentDocumentsAndReuseClass(t *testing.T) {
	a := newApp(context.Background())
	ctx, cancel := context.WithCancel(a.ctx)
	defer cancel()
	s := &arthasState{ctx: ctx, cancel: cancel}
	a.arthas = s
	firstMethod := jvmpkg.JavaMethod{ClassName: "demo.AccountController", MethodName: "profileBasic", Loader: "abc"}
	a.openJavaSource(s, firstMethod, 42)
	first := a.javaSource
	first.document = jvmpkg.SourceDocument("class AccountController {\n/*42*/ void profileBasic() {}\n}\n", nil)
	first.editor = sourceEditor(first.document.Text)
	a.positionJavaSource(first)
	a.openJavaSource(s, jvmpkg.JavaMethod{ClassName: "demo.Service", MethodName: "load", Loader: "abc"})
	second := a.javaSource
	second.editor = sourceEditor("class Service {}")
	if first == second || len(a.javaSources) != 2 || a.workspace != 3 {
		t.Fatal("source files did not open in separate tabs")
	}
	a.openJavaSource(s, firstMethod, 42)
	if a.javaSource != first || a.workspace != 2 || len(a.javaSources) != 2 {
		t.Fatal("opening an existing class did not reuse its tab")
	}
	tt := ui.NewTester(a.view, 1200, 820)
	if !tt.HasText("AccountController.java") || !tt.HasText("Service.java") || !tt.HasText(a.tr("source.code")) {
		t.Fatal("source tab or editor missing")
	}
	if err := tt.Click(a.tr("workspace.jvm")); err != nil {
		t.Fatal(err)
	}
	if a.arthas != s || ctx.Err() != nil {
		t.Fatal("switching tabs stopped the JVM session")
	}
	if err := tt.Click("AccountController.java"); err != nil {
		t.Fatal(err)
	}
	if a.selectedJavaSource() != first || len(first.highlightRows) != 1 {
		t.Fatal("switching tabs lost source position")
	}
	if err := tt.Click(a.tr("source.close_tab")); err != nil {
		t.Fatal(err)
	}
	if len(a.javaSources) != 1 || a.javaSources[0] != second || first.ctx.Err() == nil || s.ctx.Err() != nil || second.ctx.Err() != nil {
		t.Fatal("closing source tab changed another tab or the JVM session")
	}
	a.closeJavaSource(second)
}

func TestTraceSourceUsesCallerAndOriginalLineMapping(t *testing.T) {
	nodes := jvmpkg.TraceMethods(json.RawMessage(`{"className":"demo.AccountController","methodName":"profileBasic","children":[{"className":"demo.Service","methodName":"load","lineNumber":42}]}`))
	root := jvmpkg.JavaMethod{ClassName: "demo.AccountController", MethodName: "profileBasic", Loader: "abc"}
	target, line := traceSourceLocation(nodes[1], root)
	if target != root || line != 42 {
		t.Fatal("trace call-site line was attributed to the callee")
	}
	text := "package demo;\nclass AccountController {\n/*42*/ void profileBasic() {\n/*43*/     load();\n}\n}"
	doc := jvmpkg.SourceDocument(text, nil)
	v := &javaSourceViewer{method: target, line: line, document: doc, editor: sourceEditor(text)}
	a := newApp(context.Background())
	a.positionJavaSource(v)
	if len(v.highlightRows) != 1 || v.highlightRows[0] != 2 {
		t.Fatal("original source line was treated as a decompiled row")
	}
	tt := ui.NewTester(func(c *ui.Context) { a.javaSourceView(c, v) }, 1100, 760)
	if len(v.editor.LineBackgrounds) != 1 || v.editor.LineBackgrounds[0].First != 2 {
		t.Fatal("target row background missing")
	}
	if got := v.editor.Buffer.Snapshot().Pos(v.editor.Cursor()).Line; got != 2 {
		t.Fatalf("caret did not move to mapped row: %d", got)
	}
	_ = tt
	metadata := jvmpkg.SourceDocument("first\nsecond\nthird", json.RawMessage(`{"2":42,"3":null}`))
	if len(metadata.Lines[42]) != 1 || metadata.Lines[42][0] != 1 {
		t.Fatal("JAD row mapping was not applied")
	}
	v.line = 999
	a.positionJavaSource(v)
	if len(v.highlightRows) != 0 || v.status != i18npkg.MessageText("source.line_unavailable", 999) {
		t.Fatal("missing mapping highlighted a different line")
	}
}

func TestSourceLinePositionScrollsAndRendersInWorkspace(t *testing.T) {
	a := newApp(context.Background())
	a.arthas = &arthasState{ctx: a.ctx}
	var text strings.Builder
	text.WriteString("package demo;\npublic class AccountController {\n")
	for i := 1; i <= 120; i++ {
		fmt.Fprintf(&text, "/*%d*/    public String method%d() { return \"value\"; }\n", i, i)
	}
	text.WriteString("}\n")
	ctx, cancel := context.WithCancel(a.ctx)
	defer cancel()
	v := &javaSourceViewer{ctx: ctx, cancel: cancel, method: jvmpkg.JavaMethod{ClassName: "demo.AccountController"}, line: 90, document: jvmpkg.SourceDocument(text.String(), nil), editor: sourceEditor(text.String())}
	a.javaSources = []*javaSourceViewer{v}
	a.workspace = 2
	a.positionJavaSource(v)
	tt := ui.NewTester(a.view, 1200, 820)
	first, last := v.editor.VisibleLines()
	if first > 91 || last < 91 || v.editor.ScrollY() <= 0 {
		t.Fatalf("target line not visible after positioning: %d..%d", first, last)
	}
	if path := os.Getenv("WEBHOLA_SOURCE_TAB_PREVIEW"); path != "" {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(f, tt.Image())
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
}
