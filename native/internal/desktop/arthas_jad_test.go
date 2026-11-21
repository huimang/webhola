package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	jvmpkg "webhola/native/internal/jvm"
)

func jadTestResult(params, metadata string) map[string]json.RawMessage {
	return map[string]json.RawMessage{"type": json.RawMessage(`"watch"`), "value": json.RawMessage(fmt.Sprintf(`[%s,null,null,%s]`, params, metadata))}
}

func TestJadParametersMatchOverloadsAndKeepValues(t *testing.T) {
	source := `package demo;
 import java.util.Map;
 public class Service {
 public String greet(@Deprecated final String personName, int repeatCount) {return "greet(int wrong)";}
 public String greet(int count) {return "count";}
 public void greet(Map<String, java.util.List<Integer>> payload, String... labels) throws Exception {}
 public void greet(java.sql.Date sqlDate) {}
 public void greet(java.util.Date utilDate) {}
 public void greet(String[] values, long ids[]) {}
 }`
	p := jvmpkg.ParseJadParameters(source, "greet")
	for _, tc := range []struct{ params, meta, want string }{
		{`["中文",9007199254740993]`, `[{"name":null,"type":"java.lang.String"},{"name":null,"type":"int"}]`, `{"personName":"中文","repeatCount":9007199254740993}`},
		{`[2]`, `[{"name":null,"type":"int"}]`, `{"count":2}`},
		{`[{},[]]`, `[{"name":null,"type":"java.util.Map"},{"name":null,"type":"java.lang.String[]"}]`, `{"labels":[],"payload":{}}`},
		{`[null]`, `[{"name":null,"type":"java.sql.Date"}]`, `{"sqlDate":null}`},
		{`["x",1]`, `[{"name":"annotationName","type":"java.lang.String"},{"name":null,"type":"int"}]`, `{"annotationName":"x","repeatCount":1}`},
		{`[[],[]]`, `[{"name":null,"type":"java.lang.String[]"},{"name":null,"type":"long[]"}]`, `{"ids":[],"values":[]}`},
	} {
		result := jadTestResult(tc.params, tc.meta)
		r, ok := jvmpkg.NewArthasRecord(result, jvmpkg.JavaMethod{}, "", "")
		if !ok {
			t.Fatal("bad fixture")
		}
		raw := r.Raw
		p.Supplement(&r, result)
		if r.Params != tc.want || r.UnnamedParams || !r.DecompiledParams || r.Raw != raw {
			t.Fatalf("%+v", r)
		}
	}
	for _, source := range []string{`public void greet(int one){} public void greet(int two){}`, `public void greet(String wrong){}`, `// public void greet(int wrong) {}`, `public void other(){ obj.greet(1); }`} {
		result := jadTestResult(`[1]`, `[{"name":null,"type":"int"}]`)
		r, _ := jvmpkg.NewArthasRecord(result, jvmpkg.JavaMethod{}, "", "")
		jvmpkg.ParseJadParameters(source, "greet").Supplement(&r, result)
		if !r.UnnamedParams || r.Params != `{"arg0":1}` || r.DecompiledParams {
			t.Fatalf("ambiguous source assigned names: %+v", r)
		}
	}
}

func TestWatchJadFallbackOnceAndFailureDoesNotLoseRecords(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var request map[string]any
				_ = json.NewDecoder(r.Body).Decode(&request)
				command, _ := request["command"].(string)
				if !strings.Contains(command, `jad -E -c abc --source-only '^demo\.Service$'`) {
					t.Error(command)
				}
				if fail {
					w.WriteHeader(500)
					return
				}
				fmt.Fprint(w, `{"state":"SUCCEEDED","body":{"results":[{"type":"jad","source":"public void greet(String userName) {}"}]}}`)
			}))
			defer server.Close()
			c := &jvmpkg.ArthasClient{Endpoint: server.URL, Http: server.Client()}
			build := c.WatchRecordBuilder(context.Background(), jvmpkg.JavaMethod{ClassName: "demo.Service", MethodName: "greet", Loader: "abc"}, "route", "label")
			named := jadTestResult(`["x"]`, `[{"name":"existing","type":"java.lang.String"}]`)
			r, _ := build(named)
			if calls != 1 || r.Params != `{"existing":"x"}` {
				t.Fatal("existing names were overwritten")
			}
			result := jadTestResult(`["x"]`, `[{"name":null,"type":"java.lang.String"}]`)
			for i := 0; i < 2; i++ {
				r, ok := build(result)
				want := `{"userName":"x"}`
				if fail {
					want = `{"arg0":"x"}`
				}
				if !ok || r.Params != want || r.UnnamedParams != fail {
					t.Fatalf("%+v", r)
				}
			}
			if calls != 1 {
				t.Fatal("jad repeated for each event", calls)
			}
			if !fail {
				again := c.WatchRecordBuilder(context.Background(), jvmpkg.JavaMethod{ClassName: "demo.Service", MethodName: "greet", Loader: "abc"}, "route", "label")
				r, ok := again(result)
				if !ok || !r.DecompiledParams || calls != 1 {
					t.Fatal("repeat watch lost cached source names")
				}
			}
		})
	}
}
