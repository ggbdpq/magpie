package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The interactive mode's first turn went upstream while Claude Code was
// still connecting the bridge's MCP server: the request carried no tools,
// and the model answered "no tools available", or wrote its calls as words
// (#4762). The helper now says when Claude Code has listed its tools, and
// start holds the run's first message until then — no longer than
// mcpReadyWait, for a helper that never connects.

// The first message waits for the tools: not told while the helper's list
// is still off, told once magpie has the ready word.
func TestClaudeTUIFirstTurnWaitsForMCPTools(t *testing.T) {
	h, _ := tuiHelperHarness(t, "")
	said := make(chan int, 1)
	go func() { code, _ := h.askCode("rules", "hi"); said <- code }()

	var token string
	for deadline := time.Now().Add(5 * time.Second); token == ""; {
		h.s.subscription.mu.Lock()
		for k, r := range h.s.subscription.runs {
			if r.tui {
				token = k
			}
		}
		h.s.subscription.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("no run started")
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	select {
	case code := <-said:
		t.Fatalf("the first turn went out before the tools were listed: %d", code)
	default:
	}

	r := httptest.NewRequest("POST", "/_magpie/claude-mcp/"+token, strings.NewReader(`{"ready":true}`))
	r.SetPathValue("token", token)
	rec := httptest.NewRecorder()
	h.s.subscription.mcpCall(rec, r)
	if rec.Code != 200 {
		t.Fatalf("the ready word: %d %s", rec.Code, rec.Body.String())
	}
	select {
	case code := <-said:
		if code != 200 {
			t.Fatalf("%d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the turn never went out after the tools were listed")
	}

	// the helper says ready again as the run goes on (a list mid-turn
	// says it too): the word is taken once, and said so
	r2 := httptest.NewRequest("POST", "/_magpie/claude-mcp/"+token, strings.NewReader(`{"ready":true}`))
	r2.SetPathValue("token", token)
	rec = httptest.NewRecorder()
	h.s.subscription.mcpCall(rec, r2)
	if rec.Code != 200 {
		t.Fatalf("ready again: %d %s", rec.Code, rec.Body.String())
	}
}

// A helper that never lists the tools holds the first message no longer
// than the wait: the turn goes out without the tools, as it did before.
// The floor is mcpReadyWait's default; the two move together.
func TestClaudeTUIFirstTurnGoesWhenToolsNeverLoad(t *testing.T) {
	h, _ := tuiHelperHarness(t, "")
	started := time.Now()
	if code, msg := h.askCode("rules", "hi"); code != 200 {
		t.Fatalf("%d %s", code, msg)
	}
	if d := time.Since(started); d < 5*time.Second {
		t.Fatalf("the first turn didn't wait out the tools' loading: %s", d)
	}
}
