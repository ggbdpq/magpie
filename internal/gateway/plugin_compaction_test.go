package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A plugin's fetch is told when the gateway itself classified the request
// as an agent's compaction (CompactionHeader, "1"): the plugin's bridge
// can then keep the summarization turn out of its live tool validation,
// where Claude Code's replayed history otherwise dies as a 409 the client
// retried 87 times in one session (#7189). The mark is the gateway's own
// verdict: a client's X-Magpie-Compaction of its own is not passed on as
// one, and a plain turn says nothing.
func TestPluginToldTheCompaction(t *testing.T) {
	var mu sync.Mutex
	var got, vendored []http.Header
	pid := besideFake(t, "fakeco", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		// the plugin sends the headers it was given on, and the host takes
		// the magpie ones off: the vendor reads none of them
		vendored = append(vendored, r.Header.Clone())
		got = append(got, r.Header.Clone())
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range []string{
			`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"fake-claude","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`{"type":"message_stop"}`,
		} {
			io.WriteString(w, "data: "+e+"\n\n")
		}
	}))
	for _, tc := range []struct {
		name, system, body, forge string
		want                      string
	}{
		{
			name:   "claude code auto-compaction",
			system: `"You are a helpful AI assistant tasked with summarizing conversations."`,
			body:   `,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"out"},{"type":"text","text":"Your task is to create a detailed summary of the conversation so far"}]}]`,
			want:   "1",
		},
		{name: "plain turn", body: `,"messages":[{"role":"user","content":"hi"}]`},
		{
			name:  "a client's own mark is not the gateway's",
			body:  `,"messages":[{"role":"user","content":"hi"}]`,
			forge: "X-Magpie-Compaction: 1\r\n",
		},
	} {
		mu.Lock()
		got, vendored = nil, nil
		mu.Unlock()
		rec := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"`+pid+`/fake-claude","max_tokens":16`+
			func() string {
				if tc.system != "" {
					return `,"system":` + tc.system
				}
				return ""
			}()+tc.body+`}`))
		rec.Header.Set("X-Magpie-Compaction", "1")
		rr := httptest.NewRecorder()
		New().Handler().ServeHTTP(rr, rec)
		if rr.Code != 200 {
			t.Fatalf("%s: %d %s", tc.name, rr.Code, rr.Body)
		}
		mu.Lock()
		sent := slices.Clone(got)
		mu.Unlock()
		if len(sent) == 0 {
			t.Fatalf("%s: the plugin got nothing", tc.name)
		}
		for _, h := range sent {
			if g := h.Get("X-Saw-Compaction"); g != tc.want {
				t.Errorf("%s: the plugin was told compaction %q, want %q", tc.name, g, tc.want)
			}
		}
		for _, h := range vendored {
			if h.Get("X-Magpie-Compaction") != "" {
				t.Errorf("%s: the vendor got %s: %q", tc.name, provider.CompactionHeader, h.Values("X-Magpie-Compaction"))
			}
		}
	}
}
