package desktop

import (
	"bytes"
	"strconv"
	"time"

	capturepkg "webhola/native/internal/capture"
	i18npkg "webhola/native/internal/i18n"
	replaypkg "webhola/native/internal/replay"
)

func (a *app) acceptCaptured(e capturepkg.Exchange) {
	if a.replayEndpoints.Contains(e.Client, e.Server) {
		return
	}
	rawID := e.ID
	if rawID <= a.captureFloor {
		return
	}
	if a.captureIDs == nil {
		a.captureIDs = map[uint64]uint64{}
	}
	id, ok := a.captureIDs[rawID]
	if !ok {
		a.nextEntry++
		id = a.nextEntry
		a.captureIDs[rawID] = id
	}
	if rawID > capturepkg.MaxEntries {
		floor := rawID - capturepkg.MaxEntries
		if floor > a.captureFloor {
			a.captureFloor = floor
			for raw := range a.captureIDs {
				if raw <= floor {
					delete(a.captureIDs, raw)
				}
			}
		}
	}
	e.ID = id
	a.accept(e)
}

func (a *app) resend(source capturepkg.Exchange) {
	if a.replaying {
		return
	}
	req, err := replaypkg.ReplayRequest(source)
	if err != nil {
		a.failed = true
		a.status = i18npkg.ErrorMessage(err)
		return
	}
	a.replaying = true
	a.replayFrom = source.ID
	a.failed = false
	a.status = i18npkg.MessageText("replay.resending_request")
	sent := source.Request
	sent.Line = req.Method + " " + req.URL.RequestURI() + " HTTP/1.1"
	headers := req.Header.Clone()
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	headers.Set("Host", host)
	headers.Set("Connection", "close")
	if req.ContentLength > 0 || req.Method == "POST" || req.Method == "PUT" || req.Method == "PATCH" {
		headers.Set("Content-Length", strconv.FormatInt(req.ContentLength, 10))
	}
	var headerText bytes.Buffer
	_ = headers.Write(&headerText)
	sent.Headers = headerText.String()
	sent.ContentType = headers.Get("Content-Type")
	a.nextEntry++
	pending := capturepkg.Exchange{ID: a.nextEntry, Method: source.Method, URL: source.URL, Started: time.Now(), Request: sent, Replayed: true}
	a.accept(pending)
	a.selected = pending.ID
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		result, err := replaypkg.RunReplay(a.ctx, req, pending, a.replayEndpoints.Add)
		a.update(func() {
			a.replaying = false
			a.replayFrom = 0
			if err != nil {
				result.Error = i18npkg.ErrorMessage(err)
				a.failed = true
				a.status = i18npkg.MessageText("replay.resend_failed") + i18npkg.ErrorMessage(err)
			} else {
				a.status = i18npkg.MessageText("replay.resend_complete_http_d", result.Status)
			}
			a.accept(result)
		})
	}()
}
