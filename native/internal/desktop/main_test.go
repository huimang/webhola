package desktop

import (
	"context"
	"image/png"
	"os"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	capturepkg "webhola/native/internal/capture"
	discoverypkg "webhola/native/internal/discovery"
)

func TestRequestSelectionAndResponseTab(t *testing.T) {
	a := newApp(context.Background())
	a.loadDemo()
	tt := ui.NewTester(a.view, 1200, 820)
	if err := tt.Click("POST http://localhost:8080/api/preview"); err != nil {
		t.Fatal(err)
	}
	if a.selected != 2 {
		t.Fatal("selected request did not change")
	}
	if err := tt.Click("响应"); err != nil {
		t.Fatal(err)
	}
	if a.tab != "http.response" || !tt.HasText("201 · 31.0 ms") {
		t.Fatal("response not visible")
	}
	if err := tt.Click("HTTP 起始行与头部"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Key(ui.Cmd, ui.KeyC)
	if !strings.Contains(tt.Clipboard(), "HTTP/1.1 201 Created") {
		t.Fatal("selected response headers cannot be copied")
	}
	if err := tt.Click("清空记录"); err != nil {
		t.Fatal(err)
	}
	if len(a.entries) != 0 || !tt.HasText("还没有捕获到请求") {
		t.Fatal("clear failed")
	}
}

func TestWaitingResponse(t *testing.T) {
	a := newApp(context.Background())
	a.accept(capturepkg.Exchange{ID: 1, Method: "GET", URL: "/pending"})
	a.tab = "http.response"
	tt := ui.NewTester(a.view, 1200, 820)
	if !tt.HasText("尚未捕获到最终响应") {
		t.Fatal("pending response is unclear")
	}
}

func TestMySQLKind(t *testing.T) {
	cases := []struct {
		exchange capturepkg.Exchange
		want     string
	}{
		{capturepkg.Exchange{Protocol: "mysql", Method: "COM_QUERY", URL: "SELECT id FROM users"}, "SELECT"},
		{capturepkg.Exchange{Protocol: "mysql", Method: "COM_QUERY", URL: "UPDATE users SET name = 'x'"}, "UPDATE"},
		{capturepkg.Exchange{Protocol: "mysql", Method: "COM_QUERY", URL: "  insert into users values (1)"}, "INSERT"},
		{capturepkg.Exchange{Protocol: "mysql", Method: "COM_QUERY", URL: "DELETE FROM users"}, "DELETE"},
		{capturepkg.Exchange{Protocol: "mysql", Method: "COM_QUERY"}, "QUERY"},
		{capturepkg.Exchange{Protocol: "mysql", Method: "COM_PING"}, "PING"},
		{capturepkg.Exchange{Protocol: "mysql", Method: "COM_STMT_PREPARE", URL: "SELECT ?"}, "PREPARE"},
		{capturepkg.Exchange{Protocol: "mysql", Method: "COM_STMT_EXECUTE", URL: "SELECT ? (statement #1)"}, "EXECUTE"},
	}
	for _, tc := range cases {
		if got := mysqlKind(tc.exchange); got != tc.want {
			t.Fatalf("mysqlKind(%+v) = %q, want %q", tc.exchange, got, tc.want)
		}
	}
}

func TestMySQLRecordShowsShortTitle(t *testing.T) {
	const sql = "SELECT very_long_column_name FROM users WHERE id = 1"
	a := newApp(context.Background())
	a.accept(capturepkg.Exchange{ID: 1, Protocol: "mysql", Method: "COM_QUERY", URL: sql})
	tt := ui.NewTester(a.view, 1200, 820)
	if !tt.HasText("SELECT") {
		t.Fatal("request type is not shown")
	}
	if tt.HasText(sql) {
		t.Fatal("full SQL should stay out of the request title")
	}
}

func TestMySQLRequestDefaultsToSQLHighlight(t *testing.T) {
	a := newApp(context.Background())
	a.accept(capturepkg.Exchange{ID: 1, Protocol: "mysql", Method: "COM_QUERY", URL: "SELECT id FROM users",
		Request: capturepkg.Message{Line: "COM_QUERY", Body: "SELECT id FROM users", ContentType: "text/plain; charset=utf-8"}})
	ui.NewTester(a.view, 1200, 820)
	if a.bodyPreview == nil || a.bodyPreview.mode != bodyModeSQL || !a.bodyPreview.formatted {
		t.Fatalf("request body did not default to SQL highlight: %+v", a.bodyPreview)
	}
}

func TestMySQLResultRendersTable(t *testing.T) {
	a := newApp(context.Background())
	a.tab = "mysql.response"
	a.accept(capturepkg.Exchange{ID: 1, Protocol: "mysql", Method: "COM_QUERY", URL: "SELECT id, name FROM users",
		Response: &capturepkg.Message{Line: "结果集：1 行 × 2 列", Headers: "Columns: id, name", Body: "id\tname\n1\talice\n", ContentType: "text/plain; charset=utf-8", Bytes: 18}})
	tt := ui.NewTester(a.view, 1200, 820)
	if a.bodyPreview == nil || a.bodyPreview.mode != bodyModeResult {
		t.Fatalf("result body did not use the table view: %+v", a.bodyPreview)
	}
	if !a.bodyPreview.table.CellSelect || a.bodyPreview.table.CellsStyle.Stripe == ui.Transparent {
		t.Fatal("result table rows are not striped")
	}
	for _, want := range []string{"name", "alice"} {
		if !tt.HasText(want) {
			t.Fatalf("table cell %q is missing", want)
		}
	}
	if tt.HasText("id\tname") {
		t.Fatal("result set still rendered as raw text")
	}
}

func TestMySQLBinaryRequestRawAndFormat(t *testing.T) {
	const dump = "00000000  61 00 62                                          |a.b|\n"
	a := newApp(context.Background())
	a.accept(capturepkg.Exchange{ID: 1, Protocol: "mysql", Method: "COM_STMT_EXECUTE", URL: "statement #1",
		Request: capturepkg.Message{Line: "COM_STMT_EXECUTE", Binary: true, Body: dump, Headers: "Statement: SELECT * FROM users", ContentType: "text/plain; charset=utf-8"}})
	tt := ui.NewTester(a.view, 1200, 820)
	if a.bodyPreview == nil || a.bodyPreview.mode != bodyModeBinary {
		t.Fatalf("binary request body mode = %+v", a.bodyPreview)
	}
	if a.bodyPreview.rawText != dump {
		t.Fatalf("raw view lost the binary dump: %q", a.bodyPreview.rawText)
	}
	for _, label := range []string{"原文", "格式化"} {
		if !tt.HasText(label) {
			t.Fatalf("missing %q button", label)
		}
	}
	if err := tt.Click("格式化"); err != nil {
		t.Fatal(err)
	}
	if !a.bodyPreview.formatted {
		t.Fatal("prepared statement SQL is not shown by the format view")
	}
	if err := tt.Click("原文"); err != nil {
		t.Fatal(err)
	}
	if a.bodyPreview.formatted {
		t.Fatal("raw binary view was not restored")
	}
}

func TestHidePingFilter(t *testing.T) {
	a := newApp(context.Background())
	ping := capturepkg.Exchange{ID: 1, Protocol: "mysql", Method: "COM_PING"}
	query := capturepkg.Exchange{ID: 2, Protocol: "mysql", Method: "COM_QUERY", URL: "SELECT 1"}
	http := capturepkg.Exchange{ID: 3, Method: "GET", URL: "http://localhost:8080/"}
	for _, e := range []capturepkg.Exchange{ping, query, http} {
		if !a.matches(e) {
			t.Fatalf("record %d hidden by default", e.ID)
		}
	}
	a.hidePing = true
	if a.matches(ping) {
		t.Fatal("PING is still visible")
	}
	if !a.matches(query) || !a.matches(http) {
		t.Fatal("hiding PING hid other records")
	}
}

func TestHidePingCheckboxAppearsWithMySQL(t *testing.T) {
	a := newApp(context.Background())
	a.accept(capturepkg.Exchange{ID: 1, Method: "GET", URL: "http://localhost:8080/"})
	tt := ui.NewTester(a.view, 1200, 820)
	if tt.HasText("隐藏 PING") {
		t.Fatal("PING filter shown without MySQL records")
	}
	a.accept(capturepkg.Exchange{ID: 2, Protocol: "mysql", Method: "COM_PING"})
	tt = ui.NewTester(a.view, 1200, 820)
	if !tt.HasText("隐藏 PING") {
		t.Fatal("PING filter missing with MySQL records")
	}
	if err := tt.Click("隐藏 PING"); err != nil {
		t.Fatal(err)
	}
	if !a.hidePing || a.visibleCount() != 1 {
		t.Fatalf("PING filter did not hide the record: hide=%v visible=%d", a.hidePing, a.visibleCount())
	}
}

func TestMySQLBinaryRequestFormatWithoutSQL(t *testing.T) {
	const dump = "00000000  00 61 6c 69 63 65 00                             |.alice.|\n"
	a := newApp(context.Background())
	a.accept(capturepkg.Exchange{ID: 1, Protocol: "mysql", Method: "COM_STMT_EXECUTE", URL: "statement #1",
		Request: capturepkg.Message{Line: "COM_STMT_EXECUTE", Binary: true, Body: dump, RawBody: []byte{0x00, 'a', 'l', 'i', 'c', 'e', 0x00}, ContentType: "text/plain; charset=utf-8"}})
	tt := ui.NewTester(a.view, 1200, 820)
	if a.bodyPreview == nil || !a.bodyPreview.pretty || a.bodyPreview.editor == nil {
		t.Fatalf("format view is disabled for a binary request: %+v", a.bodyPreview)
	}
	if err := tt.Click("格式化"); err != nil {
		t.Fatal(err)
	}
	if !a.bodyPreview.formatted {
		t.Fatal("format view did not open")
	}
}

func TestMySQLCommandFormatEnabled(t *testing.T) {
	a := newApp(context.Background())
	a.accept(capturepkg.Exchange{ID: 1, Protocol: "mysql", Method: "COM_PING"})
	ui.NewTester(a.view, 1200, 820)
	if p := a.bodyPreview; p == nil || !p.pretty || p.mode != bodyModeCommand {
		t.Fatalf("command format view = %+v", p)
	}
}

func TestPrintableStrings(t *testing.T) {
	raw := []byte{0x00, 0x01, 'a', 'l', 'i', 'c', 'e', 0x00, 0xff, 0xfe, 'x', 'y', 0x00}
	got := printableStrings(raw, 100)
	if !strings.Contains(got, "alice") || !strings.Contains(got, "xy") {
		t.Fatalf("printable strings = %q", got)
	}
	if strings.ContainsRune(got, 0x00) {
		t.Fatal("binary noise leaked into the readable text")
	}
}

func TestOptionalPreview(t *testing.T) {
	path := os.Getenv("WEBHOLA_PREVIEW")
	if path == "" {
		t.Skip("optional preview artifact")
	}
	a := newApp(context.Background())
	a.loadDemo()
	a.tab = "http.response"
	a.deviceList = []capturepkg.Device{{ID: "1", Label: `\Device\NPF_Loopback (Loopback)`}}
	a.services = []discoverypkg.HTTPService{{Listener: discoverypkg.Listener{IP: "127.0.0.1", Port: 8080, PID: 123, Process: "node.exe"}, Status: 200}}
	a.chooseService(a.services[0].Key())
	a.scanStatus = "示例扫描结果 · 已自动填写端口并选择 loopback。"
	tt := ui.NewTester(a.view, 1200, 820)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = png.Encode(f, tt.Image()); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("格式化"); err != nil {
		t.Fatal(err)
	}
	a.openBodyFullscreen(a.bodyPreview)
	full := ui.NewTester(a.bodyFullscreenView, 1400, 900)
	ff, err := os.Create(path + "-fullscreen.png")
	if err != nil {
		t.Fatal(err)
	}
	if err = png.Encode(ff, full.Image()); err != nil {
		t.Fatal(err)
	}
	ff.Close()
	about := ui.NewTester(a.aboutView, 560, 420)
	af, err := os.Create(path + "-about.png")
	if err != nil {
		t.Fatal(err)
	}
	defer af.Close()
	if err = png.Encode(af, about.Image()); err != nil {
		t.Fatal(err)
	}
}
