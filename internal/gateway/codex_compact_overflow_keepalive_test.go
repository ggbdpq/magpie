package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// #5686 (1007cordon-ux): a summary request for a long thread is big, so
// the member doesn't answer within keepHeldAfter (3s), and the hold's
// keepalive has said the stream's 200 by the time the refusal comes. The
// overflow then reaches the compaction inside the stream, as the
// response.failed the ChatGPT backend answers an over-window input with:
// compactReply says it in err, and the recorder's status stays 200, where
// summaryTooLarge looks for a 413 or a 400/413/422 saying it. So the
// summary as text and magpie's own were both skipped, the failure counted
// toward the guard, and Codex asked the same oversized summary over — 20+
// times in app.log, none asked again as text, the thread stuck for good.

// chatgptOverflow is the ChatGPT backend's overflow refusal, the
// reporter's words.
const chatgptOverflow = "Your input exceeds the context window of this model. Please adjust your input and try again."

// slowOverflow answers a summary request only after a wait well past
// keepHeldAfter, with the overflow stream, as a member answers one too
// big to even start inside keepHeldAfter; when withSummary is set, an
// ask that carries the conversation as text is answered with a whole
// summary instead. Every body it is sent is kept.
type slowOverflow struct {
	t           *testing.T
	withSummary bool
	atOnce      bool // answers inside keepHeldAfter: the keepalive never fires
	mu          sync.Mutex
	got         [][]byte
}

func (s *slowOverflow) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.got = append(s.got, b)
	s.mu.Unlock()
	if s.withSummary && strings.Contains(string(b), "This is the conversation so far") {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, wholeSummary(provider.Responses))
		return
	}
	if !s.atOnce {
		time.Sleep(40 * time.Millisecond) // past the keepHeldAfter these tests shrink
	}
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, sse(overflowStream...))
}

func (s *slowOverflow) calls() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.got...)
}

func (s *slowOverflow) last() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.got[len(s.got)-1]
}

// overflowOn saves provider "fake" (model m1) on s, as setup does.
func overflowOn(t *testing.T, s *slowOverflow) {
	t.Helper()
	noRests()
	t.Cleanup(noRests)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(s)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Responses: up.URL + "/v1"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

// holdFast shrinks the hold's timers, so the keepalive says the stream's
// 200 while slowOverflow still says nothing.
func holdFast(t *testing.T) {
	t.Helper()
	k, w := keepHeldAfter, watchEvery
	keepHeldAfter, watchEvery = 2*time.Millisecond, time.Millisecond
	t.Cleanup(func() { keepHeldAfter, watchEvery = k, w })
}

// An overflow that arrives in the stream, after the keepalive has said
// its 200, is a summary request too large all the same: it is asked again
// as text, and the whole summary that writes is the compaction.
func TestCodexCompactOverflowAfterKeepaliveAsksAsText(t *testing.T) {
	up := &slowOverflow{t: t, withSummary: true}
	overflowOn(t, up)
	holdFast(t)
	code, body := codexPost(t, codexCompactOf("fake/m1", "thread-ov1"))
	if code != 200 {
		t.Fatalf("Codex's compaction failed: %d %s", code, body)
	}
	if got := compactedText(t, body); got != "SUMMARY" {
		t.Fatalf("summary %q", got)
	}
	if calls := up.calls(); len(calls) != 2 {
		t.Fatalf("asked %d times", len(calls))
	}
	if !strings.Contains(string(up.last()), "This is the conversation so far") {
		t.Errorf("the summary wasn't asked again as text: %.200s", up.last())
	}
}

// The conversation as text is refused the same way — a thread far over
// the window stays over it as text — and the summary is magpie's own, so
// the compaction still completes and Codex goes on, instead of asking the
// same oversized summary until the guard held the conversation.
func TestCodexCompactOverflowAfterKeepaliveCompactsLocally(t *testing.T) {
	up := &slowOverflow{t: t}
	overflowOn(t, up)
	holdFast(t)
	code, body := codexPost(t, codexCompactOf("fake/m1", "thread-ov2"))
	if code != 200 {
		t.Fatalf("Codex's compaction failed: %d %s", code, body)
	}
	sum := compactedText(t, body)
	for _, want := range []string{"fix the login bug", "now add a test", "I fixed main.go", chatgptOverflow} {
		if !strings.Contains(sum, want) {
			t.Errorf("local summary lacks %q:\n%s", want, sum)
		}
	}
	if calls := up.calls(); len(calls) != 2 {
		t.Fatalf("asked %d times", len(calls))
	}
}

// The classification is the refusal's words, not the keepalive: an
// overflow that arrives in the stream at once, the keepalive never
// having fired, is asked again as text too.
func TestCodexCompactOverflowInStreamAsksAsText(t *testing.T) {
	up := &slowOverflow{t: t, withSummary: true, atOnce: true}
	overflowOn(t, up)
	code, body := codexPost(t, codexCompactOf("fake/m1", "thread-ov3"))
	if code != 200 {
		t.Fatalf("Codex's compaction failed: %d %s", code, body)
	}
	if got := compactedText(t, body); got != "SUMMARY" {
		t.Fatalf("summary %q", got)
	}
	if calls := up.calls(); len(calls) != 2 {
		t.Fatalf("asked %d times", len(calls))
	}
}

// A refusal that says its size in its status, as a 400 before the hold,
// was already asked again as text, and stays so.
func TestCodexCompactOverflowStatusBeforeHoldAsksAsText(t *testing.T) {
	f := &fake{t: t, reply: wholeSummary(provider.Responses),
		refuse: func(body []byte) (int, string) {
			if strings.Contains(string(body), "This is the conversation so far") {
				return 0, ""
			}
			return http.StatusBadRequest, `{"error":{"message":"` + chatgptOverflow + `","type":"invalid_request_error"}}`
		}}
	setup(t, provider.Responses, f)
	code, body := codexPost(t, codexCompactOf("fake/m1", "thread-ov4"))
	if code != 200 {
		t.Fatalf("Codex's compaction failed: %d %s", code, body)
	}
	if got := compactedText(t, body); got != "SUMMARY" {
		t.Fatalf("summary %q", got)
	}
	if f.calls != 2 {
		t.Fatalf("asked %d times", f.calls)
	}
}

// An in-stream failure that says nothing of size is no summary and no
// text ask, as before: only an overflow is asked again smaller.
func TestCodexCompactOtherBrokenOffStays(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"response.created","response":{"id":"r1","status":"in_progress","output":[]}}`,
		`data: {"type":"response.failed","response":{"id":"r1","status":"failed","error":{"code":"rate_limit_exceeded","message":"Individual quota reached"}}}`)}
	setup(t, provider.Responses, f)
	code, body := codexPost(t, codexCompactOf("fake/m1", "thread-ov5"))
	if code < 400 || strings.Contains(body, magpieCompaction) {
		t.Fatalf("%d %s", code, body)
	}
	if f.calls != 1 {
		t.Errorf("asked %d times", f.calls)
	}
}
