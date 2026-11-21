package desktop

import (
	"time"

	capturepkg "webhola/native/internal/capture"
	i18npkg "webhola/native/internal/i18n"
)

func (a *app) loadDemo() {
	a.status = i18npkg.MessageText("demo.sample_data_no_live_capture_refresh_adapters")
	base := time.Now()
	for i, v := range []struct {
		method, path string
		code         int
		body         string
	}{{"GET", "/api/projects", 200, `{"projects":[{"id":1,"name":"Webhola","version":"0.1.0"}]}`}, {"POST", "/api/preview", 201, `{"id":"preview-42","status":"ready"}`}, {"GET", "/assets/app.css", 304, ""}, {"GET", "/api/missing", 404, `{"error":"Not found"}`}} {
		req := capturepkg.Message{Line: v.method + " " + v.path + " HTTP/1.1", Headers: "Host: localhost:8080\r\nAccept: application/json\r\nUser-Agent: Webhola-Demo/0.1.0\r\n"}
		if v.method == "POST" {
			req.ContentType = "application/json"
			req.Body = `{"url":"http://localhost:3000"}`
			req.Bytes = len(req.Body)
			req.Headers += "Content-Type: application/json\r\n"
		}
		e := capturepkg.Exchange{ID: uint64(i + 1), Frame: "1", Stream: "0", Method: v.method, URL: "http://localhost:8080" + v.path, Client: "127.0.0.1:53142", Server: "127.0.0.1:8080", Started: base.Add(time.Duration(i) * time.Second), Status: v.code, Duration: time.Duration(18+i*13) * time.Millisecond, Request: req, Response: &capturepkg.Message{Line: "HTTP/1.1 " + httpStatus(v.code), Headers: "Content-Type: application/json; charset=utf-8\r\nServer: local-dev\r\n", ContentType: "application/json", Body: v.body, Bytes: len(v.body)}}
		a.accept(e)
	}
}

func httpStatus(code int) string {
	switch code {
	case 200:
		return "200 OK"
	case 201:
		return "201 Created"
	case 304:
		return "304 Not Modified"
	default:
		return "404 Not Found"
	}
}
