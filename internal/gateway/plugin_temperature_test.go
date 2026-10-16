package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// tempUp is an upstream for the fake plugin that answers Chat and keeps
// whether each body it was asked with carried temperature and top_p.
type tempUp struct {
	mu    sync.Mutex
	sents []map[string]bool
}

func (u *tempUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var q map[string]any
	b, _ := io.ReadAll(r.Body)
	json.Unmarshal(b, &q)
	u.mu.Lock()
	u.sents = append(u.sents, map[string]bool{"temperature": q["temperature"] != nil, "top_p": q["top_p"] != nil})
	u.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, sse(`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`, `data: [DONE]`))
}

func (u *tempUp) sent(t *testing.T, model string) map[string]bool {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.sents) == 0 {
		t.Fatal("the plugin asked its upstream nothing")
	}
	return u.sents[len(u.sents)-1]
}

// A plugin model that refuses temperature and top_p says so where a
// catalog's does: its config model's temperature (Factory's 0.1.31, #154)
// or its models hook's capabilities.temperature (OpenCode's shape). The
// request magpie builds goes without them (#2492), as RejectsTemperature
// holds for a provider of the user's own; one said true, and one said
// nothing of, go with what the agent asked.
func TestPluginModelTemperatureSaidIsRead(t *testing.T) {
	t.Setenv("FAKE_NOTEMP", "1")
	up := &tempUp{}
	pid := besideFake(t, "notemp", up)
	h := New().Handler()
	ask := func(model string) map[string]bool {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(
			`{"model":"`+pid+`/`+model+`","temperature":0.4,"top_p":0.9,"messages":[{"role":"user","content":"hi"}]}`)))
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", model, rec.Code, rec.Body)
		}
		return up.sent(t, model)
	}
	if s := ask("fake-notemp"); s["temperature"] || s["top_p"] {
		t.Errorf("a config model saying temperature: false was asked with %v", s)
	}
	if s := ask("fake-hooktemp"); s["temperature"] || s["top_p"] {
		t.Errorf("a models hook model saying capabilities.temperature false was asked with %v", s)
	}
	if s := ask("fake-1"); !s["temperature"] || !s["top_p"] {
		t.Errorf("a model said nothing of was asked with %v, want both on", s)
	}
}
