package jvm

import (
	"encoding/json"
	"strings"
)

type TraceMethod struct {
	JavaMethod
	Caller JavaMethod
	Depth  int
	Cost   float64
	Times  int
	Line   int
	Failed bool
}

// Hide generated Spring proxy frames while promoting their business children.
// Keep the original chain intact for timing, failures and raw diagnostics.
func VisibleTraceMethods(nodes []TraceMethod) []TraceMethod {
	visible := make([]TraceMethod, 0, len(nodes))
	var hiddenDepths []int
	for _, node := range nodes {
		for len(hiddenDepths) > 0 && hiddenDepths[len(hiddenDepths)-1] >= node.Depth {
			hiddenDepths = hiddenDepths[:len(hiddenDepths)-1]
		}
		if strings.Contains(node.ClassName, "$$SpringCGLIB$$") || strings.Contains(node.ClassName, "$$EnhancerBySpringCGLIB$$") || strings.Contains(node.ClassName, "$$FastClassBySpringCGLIB$$") {
			hiddenDepths = append(hiddenDepths, node.Depth)
			continue
		}
		node.Depth = max(0, node.Depth-len(hiddenDepths))
		visible = append(visible, node)
	}
	return visible
}

func TraceMethods(root json.RawMessage) []TraceMethod {
	var nodes []TraceMethod
	var walk func(json.RawMessage, int, JavaMethod)
	walk = func(b json.RawMessage, depth int, caller JavaMethod) {
		if depth > 64 || len(nodes) >= 1000 {
			return
		}
		var n struct {
			ClassName  string            `json:"className"`
			MethodName string            `json:"methodName"`
			TotalCost  float64           `json:"totalCost"`
			Cost       float64           `json:"cost"`
			Times      int               `json:"times"`
			Line       int               `json:"lineNumber"`
			Failed     bool              `json:"throw"`
			Children   []json.RawMessage `json:"children"`
		}
		if json.Unmarshal(b, &n) != nil {
			return
		}
		if n.ClassName != "" && n.MethodName != "" {
			cost := n.TotalCost
			if cost == 0 {
				cost = n.Cost
			}
			nodes = append(nodes, TraceMethod{JavaMethod: JavaMethod{ClassName: n.ClassName, MethodName: n.MethodName}, Caller: caller, Depth: depth, Cost: cost / 1e6, Times: n.Times, Line: n.Line, Failed: n.Failed})
			caller = JavaMethod{ClassName: n.ClassName, MethodName: n.MethodName}
		}
		for _, child := range n.Children {
			walk(child, depth+1, caller)
		}
	}
	walk(root, 0, JavaMethod{})
	return nodes
}
