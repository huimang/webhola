package jvm

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	i18npkg "webhola/native/internal/i18n"
)

type JavaSourceDocument struct {
	Text string
	// Original source line -> zero-based rows in the decompiled document.
	Lines map[int][]int
}

var sourceLineAnnotation = regexp.MustCompile(`^\s*/\*\s*([0-9]+)\s*\*/`)

func SourceDocument(text string, mappings json.RawMessage) JavaSourceDocument {
	doc := JavaSourceDocument{Text: text, Lines: map[int][]int{}}
	for row, line := range strings.Split(text, "\n") {
		if match := sourceLineAnnotation.FindStringSubmatch(line); match != nil {
			original, _ := strconv.Atoi(match[1])
			if original > 0 {
				doc.Lines[original] = append(doc.Lines[original], row)
			}
		}
	}
	if len(doc.Lines) == 0 {
		var decoded map[string]*int
		_ = json.Unmarshal(mappings, &decoded)
		for row, original := range decoded {
			n, err := strconv.Atoi(row)
			if err == nil && original != nil && *original > 0 && n > 0 && n <= strings.Count(text, "\n")+1 {
				doc.Lines[*original] = append(doc.Lines[*original], n-1)
			}
		}
	}
	return doc
}

func (c *ArthasClient) SourceLoaders(ctx context.Context, class string) ([]string, error) {
	if !JavaClassName.MatchString(class) {
		return nil, i18npkg.MessageError("source.invalid_class")
	}
	r, err := c.Exec(ctx, "sc -d "+class)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, info := range SearchClassInfos(r) {
		if info.Name == class && (LoaderHash.MatchString(info.Hash) || info.Hash == "null") {
			seen[info.Hash] = true
		}
	}
	loaders := make([]string, 0, len(seen))
	for hash := range seen {
		loaders = append(loaders, hash)
	}
	sort.Strings(loaders)
	if len(loaders) == 0 {
		return nil, i18npkg.MessageError("source.class_unavailable")
	}
	return loaders, nil
}

func (c *ArthasClient) ClassSource(ctx context.Context, class, loader string) (string, error) {
	document, err := c.ClassSourceDocument(ctx, class, loader)
	return document.Text, err
}

func (c *ArthasClient) ClassSourceDocument(ctx context.Context, class, loader string) (JavaSourceDocument, error) {
	if !JavaClassName.MatchString(class) || (!LoaderHash.MatchString(loader) && loader != "null") {
		return JavaSourceDocument{}, i18npkg.MessageError("source.invalid_class")
	}
	option := ""
	if loader != "null" {
		option = "-c " + loader + " "
	}
	r, err := c.Exec(ctx, fmt.Sprintf("jad -E %s--source-only '%s'", option, "^"+regexp.QuoteMeta(class)+"$"))
	if err != nil {
		return JavaSourceDocument{}, err
	}
	source := ""
	var mappings json.RawMessage
	for _, result := range r.Body.Results {
		if RawString(result, "type") != "jad" {
			continue
		}
		text := RawString(result, "source")
		if strings.TrimSpace(text) == "" {
			continue
		}
		if source != "" {
			return JavaSourceDocument{}, i18npkg.MessageError("source.ambiguous")
		}
		source = text
		mappings = result["mappings"]
	}
	if source == "" {
		return JavaSourceDocument{}, i18npkg.MessageError("source.unavailable")
	}
	return SourceDocument(source, mappings), nil
}
