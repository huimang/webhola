package jvm

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxArthasRecords = 100

const MaxArthasHistoryBytes = 16 << 20

const maxArthasPayload = 256 << 10

type ArthasRecord struct {
	ID                             uint64
	Kind, RouteKey, RouteLabel     string
	Method                         JavaMethod
	At                             time.Time
	Cost                           float64
	Failed, Structured, Truncated  bool
	UnnamedParams                  bool
	DecompiledParams               bool
	Raw, Params, Result, Exception string
	Chain                          []TraceMethod
}

func RouteKey(r JavaRoute) string {
	return r.Mapping + "\x00" + r.Signature + "\x00" + r.ClassName + "#" + r.MethodName + "\x00" + r.Loader
}

func RouteLabel(r JavaRoute) string {
	return r.Mapping + " · " + r.ClassName + "#" + r.MethodName
}

func capArthasText(text string) (string, bool) {
	if len(text) <= maxArthasPayload {
		return text, false
	}
	end := maxArthasPayload
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end], true
}

func WatchFields(value json.RawMessage) (map[string]json.RawMessage, bool) {
	data := value
	var rendered string
	if json.Unmarshal(value, &rendered) == nil {
		// A JSON string emitted by the expression is displayed as @String[...]
		// in Arthas 4.3.5. Strip only that outer wrapper; never guess at object
		// display text, whose values can contain ambiguous brackets/commas.
		if strings.HasPrefix(rendered, "@String[") && strings.HasSuffix(rendered, "]") {
			rendered = rendered[len("@String[") : len(rendered)-1]
		}
		data = []byte(rendered)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) == nil && fields["params"] != nil && fields["returnObj"] != nil && fields["throwExp"] != nil {
		return fields, true
	}
	var old []json.RawMessage
	if json.Unmarshal(data, &old) == nil && (len(old) == 3 || len(old) == 4) {
		fields := map[string]json.RawMessage{"params": old[0], "returnObj": old[1], "throwExp": old[2]}
		if len(old) == 4 {
			fields["parameters"] = old[3]
		}
		return fields, true
	}
	return nil, false
}

func payloadText(raw json.RawMessage) string {
	// Controller methods may return an already encoded JSON body as a string.
	// Present its JSON contents; other string results remain JSON string values.
	var text string
	if json.Unmarshal(raw, &text) == nil && (strings.HasPrefix(strings.TrimSpace(text), "{") || strings.HasPrefix(strings.TrimSpace(text), "[")) && json.Valid([]byte(text)) {
		return text
	}
	return string(raw)
}

func NewArthasRecord(result map[string]json.RawMessage, method JavaMethod, key, label string) (ArthasRecord, bool) {
	kind := RawString(result, "type")
	if kind != "watch" && kind != "trace" {
		return ArthasRecord{}, false
	}
	r := ArthasRecord{Kind: kind, Method: method, RouteKey: key, RouteLabel: label, At: time.Now()}
	_ = json.Unmarshal(result["cost"], &r.Cost)
	if class := RawString(result, "className"); class != "" {
		r.Method.ClassName = class
	}
	if name := RawString(result, "methodName"); name != "" {
		r.Method.MethodName = name
	}
	if kind == "watch" && isCGLIBProxyClass(r.Method.ClassName) {
		return ArthasRecord{}, false
	}
	var ts int64
	if json.Unmarshal(result["ts"], &ts) == nil && ts > 0 {
		r.At = time.UnixMilli(ts)
	} else if stamp := RawString(result, "ts"); stamp != "" {
		if parsed, err := time.ParseInLocation("2006-01-02 15:04:05.999999999", stamp, time.Local); err == nil {
			r.At = parsed
		}
	}
	b, _ := json.MarshalIndent(result, "", "  ")
	r.Raw, r.Truncated = capArthasText(string(b))
	if kind == "watch" {
		if fields, ok := WatchFields(result["value"]); ok {
			r.Structured = true
			var clipped bool
			r.Params, clipped = capArthasText(payloadText(fields["params"]))
			r.Truncated = r.Truncated || clipped
			if named, unnamed, ok := namedWatchParams(fields["params"], fields["parameters"]); ok {
				r.Params, clipped = capArthasText(named)
				r.Truncated = r.Truncated || clipped
				r.UnnamedParams = unnamed
			}
			r.Result, clipped = capArthasText(payloadText(fields["returnObj"]))
			r.Truncated = r.Truncated || clipped
			r.Exception, clipped = capArthasText(payloadText(fields["throwExp"]))
			r.Truncated = r.Truncated || clipped
			r.Failed = strings.TrimSpace(string(fields["throwExp"])) != "null"
		}
		if strings.Contains(strings.ToLower(RawString(result, "accessPoint")), "exception") {
			r.Failed = true
		}
	} else {
		r.Chain = TraceMethods(result["root"])
		if len(r.Chain) > 0 {
			r.Cost = r.Chain[0].Cost
		}
		for _, node := range r.Chain {
			if node.Failed {
				r.Failed = true
			}
		}
	}
	return r, true
}

func isCGLIBProxyClass(class string) bool {
	for _, marker := range []string{"$$SpringCGLIB$$", "$$EnhancerBySpringCGLIB$$", "$$FastClassBySpringCGLIB$$", "$$EnhancerByCGLIB$$", "$$FastClassByCGLIB$$"} {
		if strings.Contains(class, marker) {
			return true
		}
	}
	return false
}

func namedWatchParams(params, metadata json.RawMessage) (string, bool, bool) {
	var values []json.RawMessage
	var parameters []struct {
		Name string `json:"name"`
	}
	if len(metadata) == 0 || json.Unmarshal(params, &values) != nil || json.Unmarshal(metadata, &parameters) != nil || len(values) != len(parameters) {
		return "", false, false
	}
	named := map[string]json.RawMessage{}
	unnamed := false
	// Reserve real names before assigning positional fallbacks.
	used := map[string]bool{}
	for _, p := range parameters {
		if p.Name != "" {
			if used[p.Name] {
				return "", false, false
			}
			used[p.Name] = true
		}
	}
	for i, p := range parameters {
		name := p.Name
		if name == "" {
			unnamed = true
			name = fmt.Sprintf("arg%d", i)
			for used[name] {
				name += "_"
			}
			used[name] = true
		}
		named[name] = values[i]
	}
	b, err := json.Marshal(named)
	return string(b), unnamed, err == nil
}

func (r ArthasRecord) Bytes() int {
	return len(r.Raw) + len(r.Params) + len(r.Result) + len(r.Exception) + len(r.RouteLabel) + len(r.RouteKey) + len(r.Chain)*512
}

func (r ArthasRecord) Summary() string {
	return fmt.Sprintf("%s · %.3f ms", r.At.Local().Format("15:04:05.000"), r.Cost)
}
