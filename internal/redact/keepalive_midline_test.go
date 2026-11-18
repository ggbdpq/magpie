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
// (#7012, two captures on a cut Chat stream). Mid-line, the keepalive is
// held whole: the frame stands unterminated (no "\n\n"), which reads as an
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
	if strings.Contains(got, "\n\n") {
		t.Fatalf("the cut frame was closed as a complete SSE event:\n%q", got)
	}
	if !strings.HasPrefix(got, half) {
		t.Fatalf("the half-written frame was lost:\ngot  %q\nwant prefix %q", got, half)
	}
}

// When the upstream recovers after a mid-line keepalive, the event
// completes with intact JSON and the held keepalive goes out after it.
// A write combining a keepalive and the line's tail also reports its full
// length so io.Copy never sees io.ErrShortWrite.
func TestKeepaliveHeldMidLineThenCompleted(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "text/event-stream")
	w := NewWriter(rec)
	w.WriteHeader(200)

	w.Write([]byte(`data: {"id":"gw-1","cre`))
	combined := []byte(": keepalive\nated\":123}\n\n")
	n, err := w.Write(combined)
	if err != nil || n != len(combined) {
		t.Fatalf("Write(combined) = (%d, %v), want (%d, nil)", n, err, len(combined))
	}
	w.Finish()

	want := "data: {\"id\":\"gw-1\",\"created\":123}\n\n: keepalive\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

// A TCP read boundary right before "\n\n" or before a JSON ":" is not a
// keepalive comment and must pass straight into the pending data: line.
func TestMidLineSplitBeforeNewlineOrColonNotSwallowed(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "text/event-stream")
	w := NewWriter(rec)
	w.WriteHeader(200)

	w.Write([]byte(`data: {"id"`))
	w.Write([]byte(`:"gw-1"}`))
	w.Write([]byte("\n\n"))
	w.Write([]byte(`data: {"id":"gw-2"}` + "\n\n"))
	w.Finish()

	want := "data: {\"id\":\"gw-1\"}\n\ndata: {\"id\":\"gw-2\"}\n\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("valid split frame corrupted:\ngot  %q\nwant %q", got, want)
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
