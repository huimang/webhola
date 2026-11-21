package desktop

import (
	"context"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	jvmpkg "webhola/native/internal/jvm"
)

func TestRoutesWithoutLoaderSuffixKeepDistinctTargets(t *testing.T) {
	first := jvmpkg.JavaRoute{JavaMethod: jvmpkg.JavaMethod{ClassName: "demo.Controller", MethodName: "hello", Loader: "abc"}, Mapping: "{GET [/hello]}"}
	second := first
	second.Loader = "def"
	if strings.Contains(jvmpkg.RouteLabel(first), first.Loader) || jvmpkg.RouteLabel(first) != jvmpkg.RouteLabel(second) || jvmpkg.RouteKey(first) == jvmpkg.RouteKey(second) {
		t.Fatal("route labels should hide loader identifiers while preserving distinct internal keys")
	}
	a := newApp(context.Background())
	a.arthas = &arthasState{ctx: a.ctx, routes: []jvmpkg.JavaRoute{first, second}}
	a.arthas.selectRoute(jvmpkg.RouteKey(first))
	tt := ui.NewTester(a.arthasView, 1200, 820)
	if err := tt.Click(a.tr("arthas_monitor.route_select")); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyEnter)
	if a.arthas.method.Loader != second.Loader || a.arthas.routeChoice != jvmpkg.RouteKey(second) {
		t.Fatal("identical visible labels selected the wrong class loader")
	}
	if err := tt.Click(a.tr("arthas_monitor.classloader")); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click(first.Loader); err != nil {
		t.Fatal(err)
	}
	if a.arthas.method.Loader != first.Loader {
		t.Fatal("class loader dropdown did not change the target")
	}
}
