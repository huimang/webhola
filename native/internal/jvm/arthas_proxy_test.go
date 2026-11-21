package jvm

import (
	"testing"
)

func TestVisibleTraceMethodsPromotesBusinessChildren(t *testing.T) {
	nodes := []TraceMethod{
		{JavaMethod: JavaMethod{ClassName: "demo.Controller", MethodName: "get"}, Depth: 0},
		{JavaMethod: JavaMethod{ClassName: "demo.Service$$SpringCGLIB$$0", MethodName: "get"}, Depth: 1, Cost: 9, Failed: true},
		{JavaMethod: JavaMethod{ClassName: "demo.Service$$EnhancerBySpringCGLIB$$abc", MethodName: "get"}, Depth: 2},
		{JavaMethod: JavaMethod{ClassName: "demo.Service", MethodName: "get"}, Depth: 3, Cost: 8},
		{JavaMethod: JavaMethod{ClassName: "demo.Repository", MethodName: "load"}, Depth: 4},
		{JavaMethod: JavaMethod{ClassName: "demo.Other", MethodName: "get"}, Depth: 1},
		{JavaMethod: JavaMethod{ClassName: "demo.CGLIBHelper", MethodName: "get"}, Depth: 2},
	}
	got := VisibleTraceMethods(nodes)
	if len(got) != 5 {
		t.Fatalf("unexpected frames: %+v", got)
	}
	for i, want := range []int{0, 1, 2, 1, 2} {
		if got[i].Depth != want {
			t.Fatalf("bad child or sibling indentation: %+v", got)
		}
	}
	if got[1].ClassName != "demo.Service" || got[1].Cost != 8 || nodes[1].Cost != 9 || !nodes[1].Failed || nodes[3].Depth != 3 {
		t.Fatal("business frame or original trace changed")
	}
	if len(VisibleTraceMethods([]TraceMethod{{JavaMethod: JavaMethod{ClassName: "demo.X$$SpringCGLIB$$0"}}})) != 0 {
		t.Fatal("proxy-only chain still visible")
	}
}
