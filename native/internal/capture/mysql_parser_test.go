package capture

import (
	"context"
	"encoding/binary"
	"strings"
	"sync"
	"testing"
)

func mysqlPacket(seq byte, payload []byte) string {
	header := []byte{byte(len(payload)), byte(len(payload) >> 8), byte(len(payload) >> 16), seq}
	return string(append(header, payload...))
}

func mysqlGreeting() []byte {
	payload := []byte{0x0a}
	payload = append(payload, []byte("8.0.36\x00")...)
	payload = append(payload, 0x01, 0x00, 0x00, 0x00) // thread id
	payload = append(payload, []byte("12345678")...)  // auth plugin data part 1
	payload = append(payload, 0x00)                   // filler
	payload = append(payload, 0xff, 0xff)             // capability flags lower
	payload = append(payload, 0x21)                   // charset
	payload = append(payload, 0x02, 0x00)             // status flags
	payload = append(payload, 0xff, 0x81)             // capability flags upper
	payload = append(payload, 0x15)                   // auth plugin data length
	payload = append(payload, make([]byte, 10)...)    // reserved
	payload = append(payload, []byte("abcdefghijkl\x00")...)
	payload = append(payload, []byte("mysql_native_password\x00")...)
	return payload
}

func mysqlOKPayload(affected, lastID uint64, status uint16) []byte {
	b := []byte{0x00}
	b = append(b, byte(affected), byte(lastID))
	b = append(b, byte(status), byte(status>>8))
	return append(b, 0x00, 0x00)
}

func mysqlColumnDef(name string) []byte {
	var b []byte
	for _, value := range []string{"def", "test", "users", "users", name, name} {
		b = append(b, byte(len(value)))
		b = append(b, value...)
	}
	return append(b, 0x0c, 0x21, 0x00, 0x09, 0x00, 0x00, 0x00, 0x03, 0x00, 0x00, 0x00, 0x00, 0x00)
}

func mysqlRow(values ...string) []byte {
	var b []byte
	for _, value := range values {
		b = append(b, byte(len(value)))
		b = append(b, value...)
	}
	return b
}

// connectMySQL emits a plaintext handshake without TLS or compression.
func connectMySQL(c *testConn) {
	c.handshake()
	c.server(mysqlPacket(0, mysqlGreeting()))
	caps := make([]byte, 4)
	binary.LittleEndian.PutUint32(caps, 0x00000200) // CLIENT_PROTOCOL_41
	c.client(mysqlPacket(1, caps))
	c.server(mysqlPacket(2, mysqlOKPayload(0, 0, 0x0002)))
}

func runPipelinePort(t *testing.T, packets []IPPacket, port uint16) []Exchange {
	t.Helper()
	var mu sync.Mutex
	var got []Exchange
	_, err := Process(context.Background(), &sliceSource{packets: packets}, CaptureConfig{TCPPort: port}, func(e Exchange) {
		mu.Lock()
		got = append(got, e)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	return got
}

func TestMySQLQueryResult(t *testing.T) {
	c := newTestConn()
	connectMySQL(c)
	c.client(mysqlPacket(0, append([]byte{0x03}, []byte("SELECT id FROM users")...)))
	c.server(mysqlPacket(1, []byte{0x01}))
	c.server(mysqlPacket(2, mysqlColumnDef("id")))
	c.server(mysqlPacket(3, mysqlRow("1")))
	c.server(mysqlPacket(4, mysqlRow("2")))
	c.server(mysqlPacket(5, []byte{0xfe, 0x00, 0x00, 0x00, 0x00}))

	all, _ := runPipeline(t, c.packets)
	finals := finalExchanges(all)
	if len(finals) != 1 {
		t.Fatalf("final exchanges = %d, want 1 (all=%+v)", len(finals), all)
	}
	e := finals[0]
	if e.Protocol != "mysql" || e.Method != "COM_QUERY" {
		t.Fatalf("exchange = %+v", e)
	}
	if e.URL != "SELECT id FROM users" {
		t.Fatalf("url = %q", e.URL)
	}
	if e.Status != 200 || e.Response == nil {
		t.Fatalf("response = %+v", e.Response)
	}
	if !strings.Contains(e.Response.Body, "id") || !strings.Contains(e.Response.Body, "1") || !strings.Contains(e.Response.Body, "2") {
		t.Fatalf("body = %q", e.Response.Body)
	}
}

func TestMySQLErrorResponse(t *testing.T) {
	c := newTestConn()
	connectMySQL(c)
	c.client(mysqlPacket(0, append([]byte{0x03}, []byte("SELECT * FROM missing")...)))
	errPayload := append([]byte{0xff, 0x7a, 0x04, '#', '4', '2', 'S', '0', '2'}, []byte("Table missing")...)
	c.server(mysqlPacket(1, errPayload))

	all, _ := runPipeline(t, c.packets)
	finals := finalExchanges(all)
	if len(finals) != 1 {
		t.Fatalf("final exchanges = %d, want 1", len(finals))
	}
	if finals[0].Status != 400 {
		t.Fatalf("status = %d, want 400", finals[0].Status)
	}
	if finals[0].Response == nil || finals[0].Response.Body != "Table missing" {
		t.Fatalf("response = %+v", finals[0].Response)
	}
}

func TestMySQLPrepareAndExecute(t *testing.T) {
	c := newTestConn()
	connectMySQL(c)
	c.client(mysqlPacket(0, append([]byte{0x16}, []byte("SELECT ? AS value")...)))
	prepareOK := []byte{0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00}
	c.server(mysqlPacket(1, prepareOK))
	c.server(mysqlPacket(2, mysqlColumnDef("?")))
	c.server(mysqlPacket(3, []byte{0xfe, 0x00, 0x00, 0x00, 0x00}))
	c.client(mysqlPacket(0, []byte{0x17, 0x01, 0x00, 0x00, 0x00, 0x00}))
	c.server(mysqlPacket(1, mysqlOKPayload(0, 0, 0x0002)))

	all, _ := runPipeline(t, c.packets)
	finals := finalExchanges(all)
	if len(finals) != 2 {
		t.Fatalf("final exchanges = %d, want 2", len(finals))
	}
	if finals[0].Method != "COM_STMT_PREPARE" || !strings.Contains(finals[0].URL, "SELECT ?") {
		t.Fatalf("prepare = %+v", finals[0])
	}
	if finals[1].Method != "COM_STMT_EXECUTE" || !strings.Contains(finals[1].URL, "SELECT ?") {
		t.Fatalf("execute = %+v", finals[1])
	}
}

func TestMySQLCommandWithoutHandshake(t *testing.T) {
	// The connection was established before capture started, so no SYN or
	// server greeting is available and the port is not the MySQL default.
	c := newTestConn()
	c.client(mysqlPacket(0, append([]byte{0x03}, []byte("SELECT 1")...)))
	c.server(mysqlPacket(1, []byte{0x01}))
	c.server(mysqlPacket(2, mysqlColumnDef("1")))
	c.server(mysqlPacket(3, mysqlRow("1")))
	c.server(mysqlPacket(4, []byte{0xfe, 0x00, 0x00, 0x00, 0x00}))

	all, _ := runPipeline(t, c.packets)
	finals := finalExchanges(all)
	if len(finals) != 1 {
		t.Fatalf("final exchanges = %d, want 1 (all=%+v)", len(finals), all)
	}
	if finals[0].Protocol != "mysql" || finals[0].Method != "COM_QUERY" || finals[0].URL != "SELECT 1" {
		t.Fatalf("exchange = %+v", finals[0])
	}
}

func TestMySQLDefaultPort(t *testing.T) {
	c := newTestConn()
	c.serverPort = 3306
	c.handshake()
	c.client(mysqlPacket(0, []byte{0x0e})) // COM_PING
	c.server(mysqlPacket(1, mysqlOKPayload(0, 0, 0x0002)))

	finals := finalExchanges(runPipelinePort(t, c.packets, 3306))
	if len(finals) != 1 {
		t.Fatalf("final exchanges = %d, want 1", len(finals))
	}
	if finals[0].Protocol != "mysql" || finals[0].Method != "COM_PING" || finals[0].Status != 200 {
		t.Fatalf("exchange = %+v", finals[0])
	}
}

func TestMySQLRequestMessageKeepsBinaryRaw(t *testing.T) {
	binaryMessage := mysqlRequestMessage(0x17, []byte{'a', 0x00, 'b'}, 3, "")
	if !binaryMessage.Binary || binaryMessage.Body == "" {
		t.Fatalf("binary body = %+v", binaryMessage)
	}
	if len(binaryMessage.RawBody) != 3 {
		t.Fatalf("binary raw body = %v", binaryMessage.RawBody)
	}
	textMessage := mysqlRequestMessage(0x03, []byte("SELECT 1"), 8, "SELECT 1")
	if textMessage.Binary || textMessage.Body != "SELECT 1" {
		t.Fatalf("text body = %+v", textMessage)
	}
}
