package capture

import (
	"io"
	"net"
	"strconv"
	"time"
)

// ProbeMySQL reports whether a local TCP port starts with a MySQL server
// greeting. MySQL servers send the initial handshake immediately after the
// connection is accepted, so a short read is enough to tell MySQL from HTTP.
// The probe connection is closed without sending any command.
func ProbeMySQL(port uint16) bool {
	if port == 0 {
		return false
	}
	for _, host := range []string{"127.0.0.1", "::1"} {
		if probeMySQLHost(host, port) {
			return true
		}
	}
	return false
}

func probeMySQLHost(host string, port uint16) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(int(port))), 200*time.Millisecond)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	var head [5]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return false
	}
	return mysqlGreetingPrefix(head[:])
}
