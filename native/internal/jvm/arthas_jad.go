package jvm

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"text/scanner"
	"time"
	"unicode"
)

type jadMethod struct{ types, names []string }

type jadParameters struct {
	Methods     []jadMethod
	imports     map[string]string
	packageName string
}

// Tokenize Java declarations, keeping strings and comments from introducing
// false method signatures. Unrecognized declarations simply retain argN.
func javaTokens(source string) []string {
	if len(source) > maxArthasPayload {
		return nil
	}
	var scan scanner.Scanner
	scan.Init(strings.NewReader(source))
	scan.Mode = scanner.ScanIdents | scanner.ScanStrings | scanner.ScanChars | scanner.ScanComments | scanner.SkipComments
	scan.IsIdentRune = func(r rune, i int) bool {
		return r == '$' || r == '_' || unicode.IsLetter(r) || (i > 0 && unicode.IsDigit(r))
	}
	scan.Error = func(*scanner.Scanner, string) {}
	var tokens []string
	for scan.Scan() != scanner.EOF {
		tokens = append(tokens, scan.TokenText())
	}
	return tokens
}

func withoutJavaAnnotations(tokens []string) []string {
	var out []string
	for i := 0; i < len(tokens); {
		if tokens[i] != "@" {
			out = append(out, tokens[i])
			i++
			continue
		}
		i++
		if i < len(tokens) {
			i++
		}
		for i+1 < len(tokens) && tokens[i] == "." {
			i += 2
		}
		if i < len(tokens) && tokens[i] == "(" {
			depth := 1
			i++
			for i < len(tokens) && depth > 0 {
				if tokens[i] == "(" {
					depth++
				}
				if tokens[i] == ")" {
					depth--
				}
				i++
			}
		}
	}
	return out
}

func jadParameter(tokens []string) (string, string, bool) {
	tokens = withoutJavaAnnotations(tokens)
	if len(tokens) > 0 && tokens[0] == "final" {
		tokens = tokens[1:]
	}
	i := len(tokens) - 1
	for i >= 1 && (tokens[i] == "]" || tokens[i] == "[") {
		i--
	}
	if i < 1 || !JavaMethodName.MatchString(tokens[i]) {
		return "", "", false
	}
	name := tokens[i]
	typ := append([]string{}, tokens[:i]...)
	typ = append(typ, tokens[i+1:]...)
	var erased []string
	depth := 0
	for _, v := range typ {
		if v == "<" {
			depth++
			continue
		}
		if v == ">" {
			depth--
			continue
		}
		if depth == 0 {
			erased = append(erased, v)
		}
	}
	if depth != 0 {
		return "", "", false
	}
	t := strings.ReplaceAll(strings.Join(erased, ""), "...", "[]")
	if t == "" {
		return "", "", false
	}
	return t, name, true
}

func ParseJadParameters(source, method string) jadParameters {
	p := jadParameters{imports: map[string]string{}}
	tokens := javaTokens(source)
	for i := 0; i < len(tokens); i++ {
		if tokens[i] == "package" || tokens[i] == "import" {
			kind := tokens[i]
			j := i + 1
			for j < len(tokens) && tokens[j] != ";" {
				j++
			}
			if j == len(tokens) {
				continue
			}
			name := strings.Join(tokens[i+1:j], "")
			if kind == "package" {
				p.packageName = name
			} else if !strings.Contains(name, "*") && !strings.HasPrefix(name, "static") {
				parts := strings.Split(name, ".")
				p.imports[parts[len(parts)-1]] = name
			}
		}
		if tokens[i] != method || i == 0 || i+1 >= len(tokens) || tokens[i+1] != "(" {
			continue
		}
		prev := tokens[i-1]
		if !(JavaMethodName.MatchString(prev) || prev == "]" || prev == ">") || prev == "return" || prev == "new" || prev == "throw" {
			continue
		}
		j := i + 2
		depth := 1
		for j < len(tokens) && depth > 0 {
			if tokens[j] == "(" {
				depth++
			}
			if tokens[j] == ")" {
				depth--
			}
			j++
		}
		if depth != 0 || j >= len(tokens) {
			continue
		}
		end := j - 1
		if tokens[j] == "throws" {
			for j < len(tokens) && tokens[j] != "{" && tokens[j] != ";" {
				j++
			}
		}
		if j >= len(tokens) || (tokens[j] != "{" && tokens[j] != ";") {
			continue
		}
		args := tokens[i+2 : end]
		var m jadMethod
		valid := true
		angle, paren, start := 0, 0, 0
		for k := 0; k <= len(args); k++ {
			if k < len(args) {
				switch args[k] {
				case "<":
					angle++
				case ">":
					angle--
				case "(":
					paren++
				case ")":
					paren--
				}
			}
			if k == len(args) || (args[k] == "," && angle == 0 && paren == 0) {
				if k > start {
					typ, name, ok := jadParameter(args[start:k])
					if !ok {
						valid = false
						break
					}
					m.types = append(m.types, typ)
					m.names = append(m.names, name)
				}
				start = k + 1
			}
		}
		if valid {
			p.Methods = append(p.Methods, m)
		}
	}
	return p
}

func canonicalJavaType(t string) string { return strings.ReplaceAll(t, "$", ".") }

func (p jadParameters) matches(source, runtime string) bool {
	source = canonicalJavaType(source)
	runtime = canonicalJavaType(runtime)
	if source == runtime {
		return true
	}
	suffix := ""
	for strings.HasSuffix(source, "[]") {
		source = strings.TrimSuffix(source, "[]")
		suffix += "[]"
	}
	if full, ok := p.imports[source]; ok {
		return canonicalJavaType(full)+suffix == runtime
	}
	return "java.lang."+source+suffix == runtime || (p.packageName != "" && p.packageName+"."+source+suffix == runtime)
}

func (p jadParameters) Supplement(r *ArthasRecord, result map[string]json.RawMessage) {
	if !r.UnnamedParams || !r.Structured {
		return
	}
	fields, ok := WatchFields(result["value"])
	if !ok {
		return
	}
	var metadata []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if json.Unmarshal(fields["parameters"], &metadata) != nil {
		return
	}
	var chosen *jadMethod
	for i := range p.Methods {
		m := &p.Methods[i]
		if len(m.types) != len(metadata) {
			continue
		}
		matches := true
		for j, v := range metadata {
			if v.Type == "" || !p.matches(m.types[j], v.Type) {
				matches = false
				break
			}
		}
		if matches {
			if chosen != nil {
				return
			}
			chosen = m
		}
	}
	if chosen == nil {
		return
	}
	for i := range metadata {
		if metadata[i].Name == "" {
			metadata[i].Name = chosen.names[i]
		}
	}
	b, _ := json.Marshal(metadata)
	params, unnamed, ok := namedWatchParams(fields["params"], b)
	if !ok || unnamed {
		return
	}
	r.Params, ok = capArthasText(params)
	r.Truncated = r.Truncated || ok
	r.UnnamedParams = false
	r.DecompiledParams = true
}

func (c *ArthasClient) JadParameters(ctx context.Context, m JavaMethod) jadParameters {
	if !JavaClassName.MatchString(m.ClassName) || !JavaMethodName.MatchString(m.MethodName) || !LoaderHash.MatchString(m.Loader) {
		return jadParameters{}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	r, err := c.Exec(ctx, fmt.Sprintf("jad -E -c %s --source-only '%s'", m.Loader, "^"+regexp.QuoteMeta(m.ClassName)+"$"))
	if err != nil {
		return jadParameters{}
	}
	source := ""
	for _, v := range r.Body.Results {
		if RawString(v, "type") == "jad" {
			if source != "" {
				return jadParameters{}
			}
			source = RawString(v, "source")
		}
	}
	return ParseJadParameters(source, m.MethodName)
}

// Capture signatures before watch transforms the class: retransformation may
// remove local-variable debug names. The client cache survives repeat watches.
func (c *ArthasClient) WatchRecordBuilder(ctx context.Context, method JavaMethod, key, label string) func(map[string]json.RawMessage) (ArthasRecord, bool) {
	cacheKey := method.ClassName + "\x00" + method.Loader + "\x00" + method.MethodName
	c.jadMu.Lock()
	p, found := c.jadCache[cacheKey]
	c.jadMu.Unlock()
	if !found {
		p = c.JadParameters(ctx, method)
		if len(p.Methods) > 0 {
			c.jadMu.Lock()
			if len(c.jadCache) >= 128 {
				c.jadCache = nil
			}
			if c.jadCache == nil {
				c.jadCache = map[string]jadParameters{}
			}
			c.jadCache[cacheKey] = p
			c.jadMu.Unlock()
		}
	}
	return func(result map[string]json.RawMessage) (ArthasRecord, bool) {
		r, ok := NewArthasRecord(result, method, key, label)
		if ok && r.Kind == "watch" && r.UnnamedParams && r.Method.ClassName == method.ClassName && r.Method.MethodName == method.MethodName {
			p.Supplement(&r, result)
		}
		return r, ok
	}
}
