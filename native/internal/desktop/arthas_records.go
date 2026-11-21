package desktop

import (
	jvmpkg "webhola/native/internal/jvm"
)

func (s *arthasState) currentRouteLabel() string {
	for _, r := range s.routes {
		if jvmpkg.RouteKey(r) == s.routeChoice {
			return jvmpkg.RouteLabel(r)
		}
	}
	return ""
}

func (s *arthasState) selectRoute(key string) {
	for _, r := range s.routes {
		if jvmpkg.RouteKey(r) == key {
			s.routeChoice = key
			s.method = r.JavaMethod
			s.loaderChoices = nil
			s.selectedRecord = 0
			s.preview = nil
			s.detailTab = ""
			for _, record := range s.records {
				if record.RouteKey == key {
					s.selectedRecord = record.ID
					break
				}
			}
			return
		}
	}
}

func (s *arthasState) acceptRecord(r jvmpkg.ArthasRecord) {
	s.nextRecord++
	r.ID = s.nextRecord
	s.records = append(s.records, r)
	s.historyBytes += r.Bytes()
	for len(s.records) > jvmpkg.MaxArthasRecords || (s.historyBytes > jvmpkg.MaxArthasHistoryBytes && len(s.records) > 1) {
		s.historyBytes -= s.records[0].Bytes()
		s.records[0] = jvmpkg.ArthasRecord{}
		s.records = s.records[1:]
	}
	selectedExists := false
	for _, record := range s.records {
		if record.ID == s.selectedRecord {
			selectedExists = true
			break
		}
	}
	if !selectedExists {
		s.selectedRecord = 0
		s.preview = nil
		for _, record := range s.records {
			if s.routeChoice == "" || record.RouteKey == s.routeChoice {
				s.selectedRecord = record.ID
				break
			}
		}
	}
}

func (s *arthasState) clearRecords() {
	s.records = nil
	s.historyBytes = 0
	s.selectedRecord = 0
	s.preview = nil
}

func (s *arthasState) selectedCall() *jvmpkg.ArthasRecord {
	for i := range s.records {
		if s.records[i].ID == s.selectedRecord && (s.routeChoice == "" || s.records[i].RouteKey == s.routeChoice) {
			return &s.records[i]
		}
	}
	return nil
}
