package desktop

import (
	"bytes"
	"context"
	"encoding/hex"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	capturepkg "webhola/native/internal/capture"
)

func TestJSONPrettyPreservesPayload(t *testing.T) {
	source := `{"id":9007199254740993,"value":1.2300,"flags":[true,false,null],"name":"中文","text":"quote: \""}`
	for _, contentType := range []string{"application/json", "application/json; charset=utf-8", "Application/JSON", "application/problem+json; charset=UTF-8"} {
		m := capturepkg.Message{Body: source, ContentType: contentType}
		text, pretty, note := formatBody(&m)
		if !pretty || note == "" || !strings.Contains(text, "\n  \"id\": 9007199254740993") || !strings.Contains(text, "1.2300") || m.Body != source {
			t.Fatalf("JSON formatting changed data: %s / %s", contentType, text)
		}
	}
	for _, m := range []capturepkg.Message{{ContentType: "application/json", Body: `{"broken":`}, {ContentType: "application/json", Body: source, Truncated: true}, {ContentType: "application/json", Body: strings.Repeat("[", 129) + "0" + strings.Repeat("]", 129)}, {ContentType: "text/plain", Body: source}} {
		text, pretty, _ := formatBody(&m)
		if pretty || text != m.Body {
			t.Fatal("invalid, truncated or non-JSON body must remain raw")
		}
	}
}

func TestJSONBodyHighlightCopyAndReadOnly(t *testing.T) {
	a := newApp(context.Background())
	a.loadDemo()
	a.tab = "http.response"
	tt := ui.NewTester(a.view, 1200, 820)
	if a.bodyPreview.formatted {
		t.Fatal("body should default to raw")
	}
	if err := tt.Click("HTTP 正文"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Key(ui.Cmd, ui.KeyC)
	if tt.Clipboard() != a.entries[0].Response.Body {
		t.Fatal("raw body changed")
	}
	if err := tt.Click("格式化"); err != nil {
		t.Fatal(err)
	}
	if a.bodyPreview == nil || a.bodyPreview.editor == nil || a.bodyPreview.editor.Buffer.Language() == nil {
		t.Fatal("JSON highlighter not enabled")
	}
	expected := a.bodyPreview.text
	if err := tt.Click("HTTP 正文"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Key(ui.Cmd, ui.KeyC)
	if tt.Clipboard() != expected || !strings.Contains(expected, "\n") {
		t.Fatalf("formatted JSON not copyable: %q", tt.Clipboard())
	}
	tt.Type("overwrite")
	if a.bodyPreview.editor.Buffer.String() != expected {
		t.Fatal("read-only body was edited")
	}
	if err := tt.Click("原文"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("HTTP 正文"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Key(ui.Cmd, ui.KeyC)
	if tt.Clipboard() != a.entries[0].Response.Body || a.bodyPreview.formatted {
		t.Fatal("switching back lost original body")
	}
	if err := tt.Click("清空记录"); err != nil {
		t.Fatal(err)
	}
	if a.bodyPreview != nil {
		t.Fatal("clearing requests retained body preview")
	}
}

func TestImageBodyShowsMetadataAndPreview(t *testing.T) {
	var data bytes.Buffer
	picture := image.NewRGBA(image.Rect(0, 0, 4, 3))
	for i := 0; i < len(picture.Pix); i += 4 {
		picture.Pix[i], picture.Pix[i+1], picture.Pix[i+2], picture.Pix[i+3] = 255, 0, 255, 255
	}
	if err := png.Encode(&data, picture); err != nil {
		t.Fatal(err)
	}
	raw := data.Bytes()
	a := newApp(context.Background())
	m := capturepkg.Message{Line: "HTTP/1.1 200 OK", ContentType: "image/png", Body: hex.Dump(raw), RawBody: raw, Bytes: len(raw)}
	a.bodyPreview = newBodyPreview("image-1", &m)
	p := a.bodyPreview
	if !p.image || p.imageSource == nil || p.imageWidth != 4 || p.imageHeight != 3 || p.imageMode != bodyImageInfo {
		t.Fatalf("image metadata not decoded: %+v", p)
	}
	tt := ui.NewTester(func(c *ui.Context) {
		a.bodyToolbar(c, p, false)
		a.renderBody(c, p)
	}, 1200, 820)
	for _, want := range []string{"格式：PNG", "尺寸：4 × 3 px", "大小："} {
		if !tt.HasText(want) {
			t.Fatalf("missing %s in %v", want, tt.Texts())
		}
	}
	if frameHasColor(tt.Image(), 255, 0, 255) {
		t.Fatal("image body defaulted to showing the picture")
	}
	if tt.HasText("原文") {
		t.Fatal("image body exposed a raw binary view")
	}
	if err := tt.Click("预览"); err != nil {
		t.Fatal(err)
	}
	if p.imageMode != bodyImagePreview || !tt.HasText("图片预览") || !frameHasColor(tt.Image(), 255, 0, 255) {
		t.Fatal("image preview missing")
	}
}

func frameHasColor(frame *image.RGBA, r, g, b uint8) bool {
	bounds := frame.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			pixel := frame.RGBAAt(x, y)
			if pixel.R == r && pixel.G == g && pixel.B == b {
				return true
			}
		}
	}
	return false
}

func TestBodyFullscreenKeepsContentAndCanExit(t *testing.T) {
	a := newApp(context.Background())
	m := capturepkg.Message{Line: "HTTP/1.1 200 OK", ContentType: "application/json", Body: "[" + strings.Repeat(`{"id":1},`, 300) + `{"id":2}]`}
	p := newBodyPreview("response-42", &m)
	a.bodyPreview = p
	a.openBodyFullscreen(p)
	if a.bodyFullscreen == nil || a.bodyFullscreen == p || a.bodyFullscreen.editor == p.editor {
		t.Fatal("fullscreen must own independent state")
	}
	tt := ui.NewTester(a.bodyFullscreenView, 1280, 720)
	if err := tt.Click("HTTP 正文"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Key(ui.Cmd, ui.KeyC)
	if tt.Clipboard() != m.Body[:rawDisplayLimit] {
		t.Fatal("fullscreen raw text differs from the truncated body")
	}
	if err := tt.Click("格式化"); err != nil {
		t.Fatal(err)
	}
	if !a.bodyFullscreen.formatted || p.formatted {
		t.Fatal("fullscreen changed inline view mode")
	}
	if err := tt.Click("HTTP 正文"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Key(ui.Cmd, ui.KeyC)
	if tt.Clipboard() != a.bodyFullscreen.text {
		t.Fatal("fullscreen copy omitted off-screen content")
	}
	tt.Key(0, ui.KeyEscape)
	if a.bodyFullscreen != nil {
		t.Fatal("Escape did not close fullscreen")
	}
	a.openBodyFullscreen(p)
	tt = ui.NewTester(a.bodyFullscreenView, 1280, 720)
	if err := tt.Click("退出全屏"); err != nil {
		t.Fatal(err)
	}
	if a.bodyFullscreen != nil || a.bodyPreview != p || p.source != m.Body {
		t.Fatal("exiting lost inline content")
	}
}
