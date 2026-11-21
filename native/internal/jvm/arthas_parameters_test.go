package jvm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWatchParameterNamesKeepValues(t *testing.T) {
	for _, tc := range []struct {
		Params, Metadata, Want string
		Unnamed                bool
	}{
		{`[9007199254740993,{"name":"中文"},null]`, `[{"name":"userId"},{"name":"request"},{"name":"optional"}]`, `{"optional":null,"request":{"name":"中文"},"userId":9007199254740993}`, false},
		{`["first","second"]`, `[{"name":null},{"name":"arg0"}]`, `{"arg0":"second","arg0_":"first"}`, true},
		{`[1,2]`, `[{"name":null},{"name":null}]`, `{"arg0":1,"arg1":2}`, true},
		{`[]`, `[]`, `{}`, false},
	} {
		got, unnamed, ok := namedWatchParams(json.RawMessage(tc.Params), json.RawMessage(tc.Metadata))
		if !ok || got != tc.Want || unnamed != tc.Unnamed {
			t.Fatalf("got %s / %v / %v", got, unnamed, ok)
		}
	}
	for _, metadata := range []string{`[{"name":"same"},{"name":"same"}]`, `[{"name":"onlyOne"}]`, `invalid`} {
		if _, _, ok := namedWatchParams(json.RawMessage(`[1,2]`), json.RawMessage(metadata)); ok {
			t.Fatal("ambiguous metadata must not overwrite or mislabel values", metadata)
		}
	}
	value := json.RawMessage(`{"params":[123,{"ok":true}],"parameters":[{"name":"userId"},{"name":"request"}],"returnObj":{"result":true},"throwExp":null}`)
	r, ok := NewArthasRecord(map[string]json.RawMessage{"type": json.RawMessage(`"watch"`), "value": value}, JavaMethod{}, "", "")
	if !ok || r.Params != `{"request":{"ok":true},"userId":123}` || r.Result != `{"result":true}` || r.Failed || !strings.Contains(r.Raw, `"params"`) {
		t.Fatal("named parameters changed result or raw data", r)
	}
	r, ok = NewArthasRecord(map[string]json.RawMessage{"type": json.RawMessage(`"watch"`), "value": json.RawMessage(`[[null],null,null,[{"name":"optional"}]]`)}, JavaMethod{}, "", "")
	if !ok || !r.Structured || r.Params != `{"optional":null}` || r.Result != "null" || r.Exception != "null" {
		t.Fatal("new watch tuple lost null values", r)
	}
}
