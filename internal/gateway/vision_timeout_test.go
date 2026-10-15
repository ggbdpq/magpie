package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The describer's budget covers its whole round trip through the gateway:
// the queue, a backoff pause, a retry and the seer's own thinking, a
// reasoning model's description of a screenshot alone passing two minutes
// (#2318). It is at least the budget a drawn image gets.
func TestVisionBudgetCoversTheWait(t *testing.T) {
	if visionTimeout < drawTimeout {
		t.Fatalf("the describer's budget is %s, shorter than the %s a drawn image gets: a seer behind a queue or thinking past two minutes loses the whole turn", visionTimeout, drawTimeout)
	}
}

// The budget is on the real path, and a test can narrow it: a describer
// that never answers is cut off at it, and the turn fails rather than
// hangs (#2318).
func TestVisionTimeoutCutsADeadDescriber(t *testing.T) {
	s, u := eyed(t)
	was := visionTimeout
	visionTimeout = 250 * time.Millisecond
	t.Cleanup(func() { visionTimeout = was })
	hang := make(chan struct{})
	u.hold = hang
	defer close(hang)

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"probe/text","messages":[{"role":"user","content":[{"type":"text","text":"read"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}]}]}`)))
	if rec.Code != 502 || !strings.Contains(rec.Body.String(), "no answer") {
		t.Fatalf("a dead describer: %d %s", rec.Code, rec.Body.String())
	}
}
