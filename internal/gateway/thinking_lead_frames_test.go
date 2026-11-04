package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"time"
)

// A group's held stream kept its thinking to itself until the answer came,
// so an agent's "thinking with max effort" line lasted no time at all and
// the whole thought landed at once, minutes in (#4166). Held for a refusal
// after it, the stream may still tell the agent *that* it is thinking —
// the frames that say so carry none of the try's words, so the next
// member's reply can still follow them whole (#248's guarantee kept).
func TestHeldThinkingTellsTheAgentItIsThinking(t *testing.T) {
	events := strings.SplitAfter(anthropicThoughtThenRefused, "\n\n")
	w := httptest.NewRecorder()
	h := newHoldWriter(w, true)
	h.Header().Set("Content-Type", "text/event-stream")
	h.WriteHeader(http.StatusOK)
	alive := &keptAlive{proto: provider.Anthropic, model: "group/g", told: time.Now(), streams: true}
	h.alive = alive
	h.stream = true

	// the try's message_start and the thinking block's start go by; the
	// thinking is held (a refusal may still end it, #248)
	h.Write([]byte(events[0] + events[1]))
	body := w.Body.String()
	if !strings.Contains(body, `"message_start"`) {
		t.Fatal("the agent has no message_start while the try is held")
	}
	if !strings.Contains(body, `"content_block_start"`) || !strings.Contains(body, `"thinking"`) {
		t.Fatal("the agent has no thinking block start while the try is held")
	}
	if strings.Contains(body, "The user wants a plan for") {
		t.Fatal("the thinking's words went through before the answer did")
	}

	// the refusal ends the try as before: the next member answers, and
	// nothing of this try's reply preceded it
	for _, ev := range events[2:] {
		h.Write([]byte(ev))
	}
	h.settle()
	if h.passing || !h.refused {
		t.Fatalf("refusal after thinking: passing %v refused %v", h.passing, h.refused)
	}
}
