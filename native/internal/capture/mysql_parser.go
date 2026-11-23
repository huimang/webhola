package capture

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	i18npkg "webhola/native/internal/i18n"
)

const (
	mysqlDefaultPort = 3306

	mysqlClientSSL          = 0x0800
	mysqlClientCompress     = 0x0020
	mysqlClientDeprecateEOF = 0x01000000
	mysqlStatusMoreResults  = 0x0008
)

type protocolMode int

const (
	protocolHTTP protocolMode = iota
	protocolMySQL
)

// parseClient and parseServer select the protocol once, then run the matching
// parser. Detection never consumes bytes, so HTTP streams keep their timing and
// header limits.
func (s *httpStream) parseClient() {
	if s.protocol() == protocolMySQL {
		s.parseMySQLClient()
		return
	}
	s.parseRequests()
}

func (s *httpStream) parseServer() {
	if s.protocol() == protocolMySQL {
		s.parseMySQLServer()
		return
	}
	s.parseResponses()
}

func (s *httpStream) protocol() protocolMode {
	s.protoOnce.Do(func() { s.proto = s.detectProtocol() })
	return s.proto
}

// detectProtocol uses the configured port, the MySQL server greeting (MySQL
// servers speak first, HTTP servers do not) and the client request prefix.
func (s *httpStream) detectProtocol() protocolMode {
	if s.f.mysqlPort {
		return protocolMySQL
	}
	for {
		client := s.input[0].peekAt(64)
		server := s.input[1].peekAt(5)
		if mysqlGreetingPrefix(server) {
			return protocolMySQL
		}
		if httpMethodState(client) > 0 {
			return protocolHTTP
		}
		if mysqlCommandPrefix(client) {
			return protocolMySQL
		}
		if len(server) >= 5 {
			// The server spoke first, but not with a MySQL greeting.
			return protocolHTTP
		}
		// Once one direction has ended without providing a recognisable
		// protocol, stop waiting for bytes that will never arrive.
		if len(client) > 0 && s.input[1].exhausted() {
			return protocolHTTP
		}
		if len(server) > 0 && s.input[0].exhausted() {
			return protocolHTTP
		}
		if s.input[0].exhausted() && s.input[1].exhausted() {
			return protocolHTTP
		}
		select {
		case <-s.f.notify:
		case <-s.f.ctx.Done():
			return protocolHTTP
		}
	}
}

// httpMethodState reports 1 for a matched HTTP method, -1 when the bytes cannot
// start an HTTP request, and 0 while a method name is still incomplete.
func httpMethodState(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	methods := [...]string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE", "PRI"}
	for _, m := range methods {
		if len(b) >= len(m) && string(b[:len(m)]) == m {
			return 1
		}
	}
	for _, m := range methods {
		if len(b) < len(m) && strings.HasPrefix(m, string(b)) {
			return 0
		}
	}
	return -1
}

// mysqlCommandPrefix reports whether the client starts a new MySQL command
// frame. It lets non-default MySQL ports keep working when the capture starts
// after the connection was established and the server greeting was missed.
func mysqlCommandPrefix(b []byte) bool {
	if len(b) < 5 || b[3] != 0 {
		return false
	}
	if b[0] == 0x16 && b[1] == 0x03 {
		return false // TLS record header, not a MySQL length prefix
	}
	n := int(b[0]) | int(b[1])<<8 | int(b[2])<<16
	if n < 1 || n > 16<<20 || b[4] > 0x1f {
		return false
	}
	return true
}

// mysqlGreetingPrefix reports whether the server speaks first with a MySQL
// protocol frame carrying an initial handshake (protocol version 0x0a).
func mysqlGreetingPrefix(b []byte) bool {
	if len(b) < 5 {
		return false
	}
	n := int(b[0]) | int(b[1])<<8 | int(b[2])<<16
	return b[3] == 0 && n >= 5 && b[4] == 0x0a
}

type mysqlFrame struct {
	seq     uint8
	length  int
	payload []byte
	stamp   time.Time
}

// readMySQLFrame consumes one protocol frame. Payloads larger than MaxBody are
// read but only previewed, keeping message boundaries intact.
func readMySQLFrame(r *streamInput) (mysqlFrame, error) {
	var header [4]byte
	start := r.read
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return mysqlFrame{}, err
	}
	n := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
	f := mysqlFrame{seq: header[3], length: n, stamp: r.timestamp(start)}
	keep := n
	if keep > MaxBody {
		keep = MaxBody
	}
	if keep > 0 {
		f.payload = make([]byte, keep)
		if _, err := io.ReadFull(r, f.payload); err != nil {
			return mysqlFrame{}, err
		}
	}
	if n > keep {
		if _, err := io.CopyN(io.Discard, r, int64(n-keep)); err != nil {
			return mysqlFrame{}, err
		}
	}
	return f, nil
}

func (s *httpStream) mysqlResponseFrame() (mysqlFrame, error) {
	if s.mysqlPending != nil {
		f := *s.mysqlPending
		s.mysqlPending = nil
		return f, nil
	}
	return readMySQLFrame(s.input[1])
}

func lenEncInt(b []byte, pos int) (uint64, int, bool) {
	if pos >= len(b) {
		return 0, pos, false
	}
	switch c := b[pos]; {
	case c < 0xfb:
		return uint64(c), pos + 1, true
	case c == 0xfc:
		if pos+3 > len(b) {
			return 0, pos, false
		}
		return uint64(binary.LittleEndian.Uint16(b[pos+1:])), pos + 3, true
	case c == 0xfd:
		if pos+4 > len(b) {
			return 0, pos, false
		}
		return uint64(b[pos+1]) | uint64(b[pos+2])<<8 | uint64(b[pos+3])<<16, pos + 4, true
	case c == 0xfe:
		if pos+9 > len(b) {
			return 0, pos, false
		}
		return binary.LittleEndian.Uint64(b[pos+1:]), pos + 9, true
	}
	return 0, pos, false
}

func lenEncString(b []byte, pos int) (string, int, bool) {
	if pos >= len(b) {
		return "", pos, false
	}
	if b[pos] == 0xfb {
		return "", pos + 1, true
	}
	n, next, ok := lenEncInt(b, pos)
	if !ok || n > uint64(len(b)-next) {
		return "", pos, false
	}
	return string(b[next : next+int(n)]), next + int(n), true
}

func mysqlCommandName(command byte) (string, bool) {
	switch command {
	case 0x00:
		return "COM_SLEEP", false
	case 0x01:
		return "COM_QUIT", false
	case 0x02:
		return "COM_INIT_DB", true
	case 0x03:
		return "COM_QUERY", true
	case 0x04:
		return "COM_FIELD_LIST", true
	case 0x05:
		return "COM_CREATE_DB", true
	case 0x06:
		return "COM_DROP_DB", true
	case 0x07:
		return "COM_REFRESH", true
	case 0x08:
		return "COM_SHUTDOWN", true
	case 0x09:
		return "COM_STATISTICS", true
	case 0x0a:
		return "COM_PROCESS_INFO", true
	case 0x0b:
		return "COM_CONNECT", false
	case 0x0c:
		return "COM_PROCESS_KILL", true
	case 0x0d:
		return "COM_DEBUG", true
	case 0x0e:
		return "COM_PING", true
	case 0x0f:
		return "COM_TIME", false
	case 0x10:
		return "COM_DELAYED_INSERT", false
	case 0x11:
		return "COM_CHANGE_USER", true
	case 0x12:
		return "COM_BINLOG_DUMP", false
	case 0x13:
		return "COM_TABLE_DUMP", true
	case 0x14:
		return "COM_CONNECT_OUT", false
	case 0x15:
		return "COM_REGISTER_SLAVE", true
	case 0x16:
		return "COM_STMT_PREPARE", true
	case 0x17:
		return "COM_STMT_EXECUTE", true
	case 0x18:
		return "COM_STMT_SEND_LONG_DATA", false
	case 0x19:
		return "COM_STMT_CLOSE", false
	case 0x1a:
		return "COM_STMT_RESET", true
	case 0x1b:
		return "COM_SET_OPTION", true
	case 0x1c:
		return "COM_STMT_FETCH", true
	case 0x1d:
		return "COM_DAEMON", false
	case 0x1e:
		return "COM_BINLOG_DUMP_GTID", false
	case 0x1f:
		return "COM_RESET_CONNECTION", true
	}
	return fmt.Sprintf("COM_0x%02X", command), false
}

func mysqlStatementTarget(command byte, data []byte) uint32 {
	switch command {
	case 0x17, 0x18, 0x19, 0x1a, 0x1c:
		if len(data) >= 4 {
			return binary.LittleEndian.Uint32(data)
		}
	}
	return 0
}

func (s *httpStream) mysqlCommandSQL(command byte, data []byte, statement uint32) string {
	switch command {
	case 0x02:
		return "USE " + string(data)
	case 0x03, 0x04, 0x16:
		return string(data)
	case 0x0c:
		if len(data) >= 4 {
			return "KILL " + strconv.FormatUint(uint64(binary.LittleEndian.Uint32(data)), 10)
		}
	case 0x17, 0x18, 0x19, 0x1a, 0x1c:
		if statement != 0 {
			if sql, ok := s.mysqlStatements.Load(statement); ok {
				return fmt.Sprintf("%s (statement #%d)", sql, statement)
			}
		}
	}
	return ""
}

func sqlOneLine(sql string) string {
	if sql == "" {
		return ""
	}
	line := strings.Join(strings.Fields(sql), " ")
	if r := []rune(line); len(r) > 300 {
		line = string(r[:300]) + "…"
	}
	return line
}

func mysqlRequestMessage(command byte, data []byte, length int, sql string) Message {
	m := Message{Line: mysqlCommandLabel(command), ContentType: "text/plain; charset=utf-8", Bytes: length, Truncated: length > len(data)}
	switch command {
	case 0x17, 0x18, 0x19, 0x1a, 0x1c:
		if sql != "" {
			m.Headers = "Statement: " + sql
		}
	}
	m.RawBody = append([]byte(nil), data...)
	if utf8.Valid(data) && !bytes.ContainsRune(data, 0) {
		m.Body = string(data)
	} else {
		m.Binary = true
		m.Body = hex.Dump(data)
	}
	return m
}

func mysqlCommandLabel(command byte) string {
	name, _ := mysqlCommandName(command)
	return name
}

// parseMySQLClient records each client command. Sequence id 0 starts a new
// command; other ids belong to the handshake or to multi-packet commands.
func (s *httpStream) parseMySQLClient() {
	for {
		f, err := readMySQLFrame(s.input[0])
		if err != nil {
			return
		}
		if f.seq != 0 {
			s.mysqlHandshake(f)
			if s.mysqlStop.Load() {
				return
			}
			continue
		}
		if f.length == 0 {
			continue
		}
		command := f.payload[0]
		name, expectsResponse := mysqlCommandName(command)
		statement := mysqlStatementTarget(command, f.payload[1:])
		sql := s.mysqlCommandSQL(command, f.payload[1:], statement)
		url := sqlOneLine(sql)
		if url == "" && statement != 0 {
			url = fmt.Sprintf("statement #%d", statement)
		}
		id := s.f.next.Add(1)
		e := Exchange{
			ID: id, Frame: fmt.Sprintf("request-%d", id), Stream: strconv.FormatUint(s.id, 10),
			Protocol: "mysql", Method: name, URL: url,
			Client: s.client, Server: s.server, Started: f.stamp,
		}
		message := mysqlRequestMessage(command, f.payload[1:], f.length-1, sql)
		if !expectsResponse {
			reply := Message{Line: i18npkg.MessageText("mysql.no_response_expected"), ContentType: "text/plain; charset=utf-8"}
			e.Request = message
			e.Response = &reply
			s.f.publish(e)
			continue
		}
		x := &capturedRequest{command: command, sql: sql, statement: statement}
		x.exchange = e
		select {
		case s.requests <- x:
		case <-s.f.ctx.Done():
			return
		default:
			s.f.diag.resourceDrops.Add(1)
			s.abortWith(true)
			return
		}
		x.mu.Lock()
		x.exchange.Request = message
		x.complete = true
		s.f.publish(x.snapshot())
		x.mu.Unlock()
	}
}

func (s *httpStream) mysqlHandshake(f mysqlFrame) {
	if f.seq != 1 || len(f.payload) < 4 {
		return
	}
	caps := binary.LittleEndian.Uint32(f.payload[:4])
	s.mysqlCaps.Store(caps)
	if caps&(mysqlClientSSL|mysqlClientCompress) != 0 {
		s.mysqlStop.Store(true)
		s.f.log("mysql stream=%d %s -> %s negotiated TLS or compression; frame parsing stopped", s.id, s.client, s.server)
	}
}

type mysqlReply struct {
	status   int
	message  Message
	duration time.Duration
}

// parseMySQLServer pairs one response cycle with each recorded command.
func (s *httpStream) parseMySQLServer() {
	for {
		var x *capturedRequest
		select {
		case <-s.f.ctx.Done():
			return
		case next, ok := <-s.requests:
			if !ok {
				return
			}
			x = next
		}
		if s.mysqlStop.Load() {
			return
		}
		reply, err := s.mysqlResponse(x)
		if err != nil {
			if s.f.ctx.Err() == nil {
				s.f.diag.parseErrors.Add(1)
				s.f.log("mysql parse error stream=%d %s -> %s: %v", s.id, s.client, s.server, err)
			}
			s.abortWith(true)
			return
		}
		x.mu.Lock()
		// The PREPARE response may be parsed after the EXECUTE command was
		// recorded, so resolve the statement text now that IDs are known.
		if x.statement != 0 && x.sql == "" {
			if value, ok := s.mysqlStatements.Load(x.statement); ok {
				if sql, _ := value.(string); sql != "" {
					x.sql = sql
					x.exchange.URL = sqlOneLine(fmt.Sprintf("%s (statement #%d)", sql, x.statement))
					x.exchange.Request.Headers = "Statement: " + sql
				}
			}
		}
		x.exchange.Response = &reply.message
		x.exchange.Status = reply.status
		x.exchange.Duration = reply.duration
		if x.complete {
			s.f.publish(x.snapshot())
		}
		x.mu.Unlock()
	}
}

func (s *httpStream) mysqlResponse(x *capturedRequest) (mysqlReply, error) {
	deprecateEOF := s.mysqlCaps.Load()&mysqlClientDeprecateEOF != 0
	var first mysqlFrame
	found := false
	// Skip the greeting and authentication packets. Every command response
	// starts at sequence id 1.
	for i := 0; i < 64 && !found; i++ {
		if s.mysqlStop.Load() {
			return mysqlReply{}, fmt.Errorf("MySQL connection uses TLS or compression")
		}
		f, err := s.mysqlResponseFrame()
		if err != nil {
			return mysqlReply{}, err
		}
		if f.seq == 1 {
			first, found = f, true
		}
	}
	if !found {
		return mysqlReply{}, fmt.Errorf("MySQL response sequence not found")
	}
	reply, err := s.mysqlCycle(x, first, deprecateEOF)
	if err != nil {
		return mysqlReply{}, err
	}
	if s.input[1].read > 0 {
		if end := s.input[1].timestamp(s.input[1].read - 1); end.After(x.exchange.Started) {
			reply.duration = end.Sub(x.exchange.Started)
		}
	}
	return reply, nil
}

func (s *httpStream) mysqlCycle(x *capturedRequest, first mysqlFrame, deprecateEOF bool) (mysqlReply, error) {
	switch {
	case x.command == 0x09:
		return mysqlStatistics(first), nil
	case x.command == 0x04:
		return s.mysqlFieldList(first)
	case x.command == 0x16 && len(first.payload) > 0 && first.payload[0] == 0x00:
		return s.mysqlPrepare(first, x, deprecateEOF)
	}
	binaryRows := x.command == 0x17 || x.command == 0x1c
	frame := first
	total := 0
	rows := 0
	var names []string
	var preview strings.Builder
	var parts []string
	for {
		if len(frame.payload) == 0 {
			return mysqlReply{}, fmt.Errorf("empty MySQL response packet")
		}
		total += frame.length
		more := false
		switch frame.payload[0] {
		case 0xff:
			return mysqlReply{status: 400, message: mysqlERRMessage(frame.payload, frame.length)}, nil
		case 0xfb:
			return mysqlReply{}, fmt.Errorf("MySQL LOCAL INFILE is not supported")
		case 0x00:
			text, next := mysqlOKText(frame.payload)
			parts = append(parts, text)
			more = next
		case 0xfe:
			parts = append(parts, i18npkg.MessageText("mysql.ok"))
		default:
			set, err := s.mysqlRows(frame, deprecateEOF, binaryRows)
			if err != nil {
				return mysqlReply{}, err
			}
			total += set.bytes
			rows += set.rows
			if len(names) == 0 && len(set.names) > 0 {
				names = set.names
			}
			if preview.Len() < MaxBody {
				preview.WriteString(set.preview.String())
			}
			parts = append(parts, i18npkg.MessageText("mysql.result", set.rows, set.columns))
			more = set.more
		}
		if !more {
			break
		}
		next, err := s.mysqlResponseFrame()
		if err != nil {
			return mysqlReply{}, err
		}
		frame = next
	}
	body := preview.String()
	if body == "" && binaryRows && rows > 0 {
		body = i18npkg.MessageText("mysql.binary_rows")
	}
	message := Message{
		Line:        strings.Join(parts, " · "),
		ContentType: "text/plain; charset=utf-8",
		Body:        body,
		RawBody:     []byte(body),
		Bytes:       total,
		Truncated:   total > MaxBody,
	}
	if len(names) > 0 {
		message.Headers = "Columns: " + strings.Join(names, ", ")
	}
	return mysqlReply{status: 200, message: message}, nil
}

func mysqlStatistics(f mysqlFrame) mysqlReply {
	if len(f.payload) > 0 && f.payload[0] == 0xff {
		return mysqlReply{status: 400, message: mysqlERRMessage(f.payload, f.length)}
	}
	body := string(f.payload)
	return mysqlReply{status: 200, message: Message{
		Line:        i18npkg.MessageText("mysql.statistics"),
		ContentType: "text/plain; charset=utf-8",
		Body:        body,
		RawBody:     append([]byte(nil), f.payload...),
		Bytes:       f.length,
		Truncated:   f.length > len(f.payload),
	}}
}

func mysqlOKText(p []byte) (string, bool) {
	pos := 1
	affected, next, ok := lenEncInt(p, pos)
	if !ok {
		return i18npkg.MessageText("mysql.ok"), false
	}
	pos = next
	lastID, next, _ := lenEncInt(p, pos)
	pos = next
	var status, warnings uint16
	if pos+2 <= len(p) {
		status = binary.LittleEndian.Uint16(p[pos:])
	}
	if pos+4 <= len(p) {
		warnings = binary.LittleEndian.Uint16(p[pos+2:])
	}
	parts := []string{i18npkg.MessageText("mysql.ok")}
	if affected > 0 {
		parts = append(parts, i18npkg.MessageText("mysql.rows_affected", affected))
	}
	if lastID > 0 {
		parts = append(parts, i18npkg.MessageText("mysql.insert_id", lastID))
	}
	if warnings > 0 {
		parts = append(parts, i18npkg.MessageText("mysql.warnings", warnings))
	}
	return strings.Join(parts, " · "), status&mysqlStatusMoreResults != 0
}

func mysqlERRMessage(p []byte, length int) Message {
	code := 0
	if len(p) >= 3 {
		code = int(binary.LittleEndian.Uint16(p[1:]))
	}
	state, pos := "", 3
	if len(p) >= 9 && p[3] == '#' {
		state = string(p[4:9])
		pos = 9
	}
	text := string(p[pos:])
	line := i18npkg.MessageText("mysql.err_no_state", code, text)
	if state != "" {
		line = i18npkg.MessageText("mysql.err", code, state, text)
	}
	return Message{
		Line:        line,
		ContentType: "text/plain; charset=utf-8",
		Body:        text,
		RawBody:     append([]byte(nil), p...),
		Bytes:       length,
		Truncated:   length > len(p),
	}
}

type mysqlSet struct {
	columns int
	rows    int
	bytes   int
	names   []string
	preview strings.Builder
	more    bool
}

func (s *httpStream) mysqlRows(first mysqlFrame, deprecateEOF, binary bool) (mysqlSet, error) {
	var set mysqlSet
	cols, _, ok := lenEncInt(first.payload, 0)
	if !ok || cols == 0 || cols > 4096 {
		return set, fmt.Errorf("invalid MySQL column count")
	}
	set.columns = int(cols)
	for i := uint64(0); i < cols; i++ {
		f, err := s.mysqlResponseFrame()
		if err != nil {
			return set, err
		}
		set.bytes += f.length
		if i < 64 {
			if name := mysqlColumnName(f.payload); name != "" {
				set.names = append(set.names, name)
			}
		}
	}
	set.writeHeader()
	var pending *mysqlFrame
	if !deprecateEOF {
		f, err := s.mysqlResponseFrame()
		if err != nil {
			return set, err
		}
		if mysqlEOFPacket(f.payload) {
			set.bytes += f.length
		} else {
			// DEPRECATE_EOF was negotiated but not visible in this capture.
			pending = &f
		}
	}
	if pending != nil {
		set.addRow(*pending, binary)
	}
	for {
		f, err := s.mysqlResponseFrame()
		if err != nil {
			return set, err
		}
		if mysqlEOFPacket(f.payload) {
			set.bytes += f.length
			set.more = mysqlMoreResults(f.payload)
			return set, nil
		}
		set.addRow(f, binary)
	}
}

func (s *mysqlSet) writeHeader() {
	if len(s.names) == 0 {
		return
	}
	names := make([]string, len(s.names))
	for i, name := range s.names {
		names[i] = mysqlCell(name)
	}
	s.writeLine(strings.Join(names, "\t"))
}

func (s *mysqlSet) writeLine(line string) {
	if s.preview.Len() >= MaxBody {
		return
	}
	line += "\n"
	if left := MaxBody - s.preview.Len(); len(line) > left {
		line = line[:left]
	}
	s.preview.WriteString(line)
}

func (s *mysqlSet) addRow(f mysqlFrame, binary bool) {
	s.bytes += f.length
	s.rows++
	if binary || s.preview.Len() >= MaxBody {
		return
	}
	cells := mysqlTextRow(f.payload, s.columns)
	if cells == nil {
		return
	}
	s.writeLine(strings.Join(cells, "\t"))
}

func mysqlTextRow(p []byte, columns int) []string {
	cells := make([]string, 0, columns)
	pos := 0
	for i := 0; i < columns; i++ {
		if pos >= len(p) {
			cells = append(cells, "")
			continue
		}
		if p[pos] == 0xfb {
			cells = append(cells, "NULL")
			pos++
			continue
		}
		value, next, ok := lenEncString(p, pos)
		if !ok {
			return nil
		}
		cells = append(cells, mysqlCell(value))
		pos = next
	}
	return cells
}

func mysqlCell(value string) string {
	value = strings.Map(func(r rune) rune {
		switch r {
		case '\t', '\r', '\n':
			return ' '
		}
		return r
	}, value)
	if r := []rune(value); len(r) > 256 {
		value = string(r[:256]) + "…"
	}
	return value
}

func mysqlColumnName(p []byte) string {
	pos := 0
	for i := 0; i < 5; i++ {
		value, next, ok := lenEncString(p, pos)
		if !ok {
			return ""
		}
		pos = next
		if i == 4 {
			return value
		}
	}
	return ""
}

func mysqlEOFPacket(p []byte) bool {
	return len(p) > 0 && p[0] == 0xfe && len(p) < 9
}

func mysqlMoreResults(p []byte) bool {
	if len(p) < 5 {
		return false
	}
	return binary.LittleEndian.Uint16(p[3:])&mysqlStatusMoreResults != 0
}

func (s *httpStream) mysqlPrepare(first mysqlFrame, x *capturedRequest, deprecateEOF bool) (mysqlReply, error) {
	p := first.payload
	if len(p) < 9 {
		return mysqlReply{}, fmt.Errorf("truncated MySQL prepare response")
	}
	statement := binary.LittleEndian.Uint32(p[1:])
	columns := int(binary.LittleEndian.Uint16(p[5:]))
	params := int(binary.LittleEndian.Uint16(p[7:]))
	if x.sql != "" {
		s.mysqlStatements.Store(statement, x.sql)
	}
	total := first.length
	skip := func(n int) error {
		for i := 0; i < n; i++ {
			f, err := s.mysqlResponseFrame()
			if err != nil {
				return err
			}
			total += f.length
		}
		return nil
	}
	if err := skip(params); err != nil {
		return mysqlReply{}, err
	}
	if params > 0 && !deprecateEOF {
		f, err := s.mysqlResponseFrame()
		if err != nil {
			return mysqlReply{}, err
		}
		if mysqlEOFPacket(f.payload) {
			total += f.length
		} else {
			s.mysqlPending = &f
		}
	}
	if err := skip(columns); err != nil {
		return mysqlReply{}, err
	}
	if columns > 0 && !deprecateEOF {
		f, err := s.mysqlResponseFrame()
		if err != nil {
			return mysqlReply{}, err
		}
		if mysqlEOFPacket(f.payload) {
			total += f.length
		} else {
			s.mysqlPending = &f
		}
	}
	message := Message{
		Line:        i18npkg.MessageText("mysql.prepared", statement, columns, params),
		ContentType: "text/plain; charset=utf-8",
		Body:        x.sql,
		RawBody:     []byte(x.sql),
		Bytes:       total,
	}
	return mysqlReply{status: 200, message: message}, nil
}

func (s *httpStream) mysqlFieldList(first mysqlFrame) (mysqlReply, error) {
	total := 0
	fields := 0
	var names []string
	frame := first
	for {
		if len(frame.payload) > 0 && frame.payload[0] == 0xff {
			return mysqlReply{status: 400, message: mysqlERRMessage(frame.payload, frame.length)}, nil
		}
		total += frame.length
		if mysqlEOFPacket(frame.payload) {
			break
		}
		fields++
		if len(names) < 64 {
			if name := mysqlColumnName(frame.payload); name != "" {
				names = append(names, name)
			}
		}
		next, err := s.mysqlResponseFrame()
		if err != nil {
			return mysqlReply{}, err
		}
		frame = next
	}
	body := strings.Join(names, "\n")
	return mysqlReply{status: 200, message: Message{
		Line:        i18npkg.MessageText("mysql.fields", fields),
		ContentType: "text/plain; charset=utf-8",
		Body:        body,
		RawBody:     []byte(body),
		Bytes:       total,
	}}, nil
}
