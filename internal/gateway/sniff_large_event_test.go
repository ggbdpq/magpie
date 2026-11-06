package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// bigCompleted is a Responses stream's last event carrying n tool
// definitions, as a reply echoes the tools it was asked with: over a
// thousand of them take such an event past 1 MiB.
func bigCompleted(t *testing.T, n int) string {
	t.Helper()
	tools := make([]map[string]any, n)
	for i := range tools {
		tools[i] = map[string]any{
			"type":        "function",
			"name":        fmt.Sprintf("mcp__connector__tool_%d", i),
			"description": strings.Repeat("Reads one record of the connector. ", 40),
			"parameters":  map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}},
		}
	}
	b, err := json.Marshal(map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id": "resp_big", "status": "completed", "model": "gpt-6-astra", "tools": tools,
			"usage": map[string]any{
				"input_tokens":          313185,
				"input_tokens_details":  map[string]any{"cached_tokens": 7936},
				"output_tokens":         107,
				"output_tokens_details": map[string]any{"reasoning_tokens": 14},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return "event: response.completed\ndata: " + string(b) + "\n\n"
}

// inChunks writes s as the gateway's copy loop hands a stream over.
func inChunks(s *usageSniffer, b string) {
	for len(b) > 0 {
		n := min(32<<10, len(b))
		s.write([]byte(b[:n]))
		b = b[n:]
	}
}

// A Codex turn that sends over a thousand tools gets them back in
// response.completed. The usage in that event is the turn's, however long
// the event is (#5505).
func TestSniffUsagePastOneMiB(t *testing.T) {
	stream := bigCompleted(t, 1102)
	if len(stream) <= 1<<20 {
		t.Fatalf("the event is %d bytes, not past the old cap", len(stream))
	}
	s := newSniffer(provider.Responses, "text/event-stream")
	inChunks(s, stream)
	if !s.whole() {
		t.Fatal("the stream's last event wasn't seen")
	}
	u := s.usage()
	if u.Input != 313185-7936 || u.CacheRead != 7936 || u.Output != 107 || u.ResponseID != "resp_big" {
		t.Fatalf("usage %+v", u)
	}
}

// An event past the cap is still let go, whole, with nothing of it kept,
// and what the stream says after it is read.
func TestSniffEventPastTheCapIsLetGo(t *testing.T) {
	s := newSniffer(provider.Responses, "text/event-stream")
	huge := `data: {"type":"response.output_item.done","item":"` + strings.Repeat("x", sniffEventMost+1) + `"}` + "\n\n"
	inChunks(s, huge[:len(huge)-2])
	if len(s.buf) > sniffEventMost || len(s.data) > sniffEventMost {
		t.Fatalf("kept %d and %d bytes past the cap", len(s.buf), len(s.data))
	}
	inChunks(s, "\n\n"+`data: {"type":"response.completed","response":{"id":"r2","usage":{"input_tokens":9,"output_tokens":2}}}`+"\n\n")
	if len(s.buf) != 0 || len(s.data) != 0 {
		t.Fatalf("kept %d and %d bytes of a dropped event", len(s.buf), len(s.data))
	}
	if u := s.usage(); u.Input != 9 || u.Output != 2 || u.ResponseID != "r2" {
		t.Fatalf("usage after the dropped event %+v", u)
	}
}

// The whole path: a Codex turn relayed to the ChatGPT backend is in the
// ledger with its tokens when its last event is long, not as a 200 of none.
func TestCodexTurnWithALongLastEventCountsItsTokens(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	stream := bigCompleted(t, 1102)
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, stream)
	})
	codexChatGPTAuth(t, "me@example.com")
	s := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":"hi"}`))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("session_id", "long-last-event")
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Body.Len() != len(stream) {
		t.Fatalf("%d, %d of %d bytes", rec.Code, rec.Body.Len(), len(stream))
	}
	u := usage.Load(time.Time{})
	if len(u) != 1 || u[0].Input != 313185-7936 || u[0].CacheRead != 7936 || u[0].Output != 107 {
		t.Fatalf("usage %+v", u)
	}
}
