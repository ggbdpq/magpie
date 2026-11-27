package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #8018: a body of a MiB or more to Qianfan asks the upstream to wait
// for the 100 first (Expect: 100-continue) — its gateway resets a bigger
// one sent straight away over HTTP/1.1. A smaller body, and another
// vendor's request whatever its size, go as they were.
func TestQianfanLargeBodyExpectsContinue(t *testing.T) {
	reply := `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	for _, tc := range []struct {
		name   string
		preset string
		size   int
		want   bool
	}{
		{"a MiB body asks for the 100", "baidu-qianfan", 1<<20 + 64, true},
		{"a small body goes as it came", "baidu-qianfan", 1 << 12, false},
		{"another vendor goes as it came", "", 1<<20 + 64, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var expect string
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				expect = r.Header.Get("Expect")
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, reply)
			}))
			t.Cleanup(up.Close)
			noRests()
			t.Cleanup(noRests)
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			p := provider.Provider{ID: "qn", Name: "Qianfan", Preset: tc.preset, Anthropic: up.URL, Models: []string{"m"}}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			body := `{"model":"qn/m","max_tokens":32,"messages":[{"role":"user","content":"` + strings.Repeat("a", tc.size) + `"}]}`
			rec := httptest.NewRecorder()
			New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
			if rec.Code != 200 {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			mu.Lock()
			defer mu.Unlock()
			if has := expect == "100-continue"; has != tc.want {
				t.Fatalf("Expect %q, want %v", expect, tc.want)
			}
		})
	}
}
