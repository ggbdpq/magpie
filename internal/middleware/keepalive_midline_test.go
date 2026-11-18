package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Brother of internal/redact.Writer (#7012): an onEvent middleware's SSE
// writer buffers the stream the same way and must hold a mid-line
// ": keepalive" instead of splicing it into a pending data: line.
func TestKeepaliveHeldMidLine(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "text/event-stream")
	w := &writer{w: rec, run: &Run{set: &set{}}, ev: true, status: http.StatusOK}
	w.WriteHeader(200)

	half := `data: {"id":"gw-1","object":"chat.completion.chunk","cre`
	w.Write([]byte(half))
	w.Write([]byte(": keepalive\n"))
	w.Write([]byte("\n"))
	w.finish()

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

func TestKeepaliveHeldMidLineThenCompleted(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "text/event-stream")
	w := &writer{w: rec, run: &Run{set: &set{}}, ev: true, status: http.StatusOK}
	w.WriteHeader(200)

	w.Write([]byte(`data: {"id":"gw-1","cre`))
	combined := []byte(": keepalive\nated\":123}\n\n")
	n, err := w.Write(combined)
	if err != nil || n != len(combined) {
		t.Fatalf("Write(combined) = (%d, %v), want (%d, nil)", n, err, len(combined))
	}
	w.finish()

	want := "data: {\"id\":\"gw-1\",\"created\":123}\n\n: keepalive\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestMidLineSplitBeforeNewlineOrColonNotSwallowed(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "text/event-stream")
	w := &writer{w: rec, run: &Run{set: &set{}}, ev: true, status: http.StatusOK}
	w.WriteHeader(200)

	w.Write([]byte(`data: {"id"`))
	w.Write([]byte(`:"gw-1"}`))
	w.Write([]byte("\n\n"))
	w.Write([]byte(`data: {"id":"gw-2"}` + "\n\n"))
	w.finish()

	want := "data: {\"id\":\"gw-1\"}\n\ndata: {\"id\":\"gw-2\"}\n\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("valid split frame corrupted:\ngot  %q\nwant %q", got, want)
	}
}

func TestKeepaliveStillPassesAtLineBoundaries(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "text/event-stream")
	w := &writer{w: rec, run: &Run{set: &set{}}, ev: true, status: http.StatusOK}
	w.WriteHeader(200)

	w.Write([]byte("data: {\"id\":\"a\"}\n\n"))
	w.Write([]byte(": keepalive\n\n"))
	w.Write([]byte("data: {\"id\":\"b\"}\n\n"))
	w.finish()

	got := rec.Body.String()
	if !strings.Contains(got, ": keepalive") {
		t.Fatalf("the keepalive between events was held:\n%q", got)
	}
	if strings.Count(got, "data: {") != 2 {
		t.Fatalf("the boundary events changed:\n%q", got)
	}
}
