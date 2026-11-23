package capture

import "time"

const MaxBody = 64 * 1024

const MaxEntries = 1000

type Message struct {
	RawBody                          []byte
	Line, Headers, Body, ContentType string
	Bytes                            int
	Truncated                        bool
	// Binary marks a body that is not displayable text; Body holds its hex
	// dump so the raw view can still show the bytes.
	Binary bool
}

type Exchange struct {
	Replayed bool
	Error    string
	ID       uint64
	// Protocol is empty for HTTP records and "mysql" for MySQL command records.
	Protocol                                   string
	Frame, Stream, Method, URL, Client, Server string
	Started                                    time.Time
	Status                                     int
	Duration                                   time.Duration
	Request                                    Message
	Response                                   *Message
	Interim                                    []int
}

type Device struct {
	ID, Label string
	// Preferred marks adapters recommended for web development, shown above
	// VPN and virtual adapters in the picker.
	Preferred bool
}
