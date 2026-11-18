package redact

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// A keepalive that arrives while the buffer stands inside a data: line —
// the relay hands over whatever a read returned, mid-line as often as not
// — was appended to the pending line, and the next blank line closed the
// frame as truncated JSON the client refuses whole and will not retry
// (#7012, two captures on a cut Chat stream). Mid-line, the comment is
// held whole: the frame stands unterminated, which reads as an
// incomplete stream and retries.
func TestKeepaliveHeldMidLine(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "text/event-stream")
	w := NewWriter(rec)
	w.WriteHeader(200)

	half := `data: {"id":"gw-1","object":"chat.completion.chunk","cre`
	w.Write([]byte(half))
	w.Write([]byte(": keepalive\n"))
	w.Write([]byte("\n"))
	w.Finish()

	got := rec.Body.String()
	if strings.Contains(got, ": keepalive") {
		t.Fatalf("the keepalive landed inside the pending data line:\n%q", got)
	}
	if got != half {
		t.Fatalf("the half-written frame stands as itself:\ngot  %q\nwant %q", got, half)
	}
}

// A keepalive between two events still goes through at a line boundary,
// ahead of the event it arrived with (#2402's proxy silence).
func TestKeepaliveStillPassesAtLineBoundaries(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "text/event-stream")
	w := NewWriter(rec)
	w.WriteHeader(200)

	w.Write([]byte("data: {\"id\":\"a\"}\n\n"))
	w.Write([]byte(": keepalive\n\n"))
	w.Write([]byte("data: {\"id\":\"b\"}\n\n"))
	w.Finish()

	got := rec.Body.String()
	if !strings.Contains(got, ": keepalive") {
		t.Fatalf("the keepalive between events was held:\n%q", got)
	}
	if strings.Count(got, "data: {") != 2 {
		t.Fatalf("the boundary events changed:\n%q", got)
	}
}
