package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// limitedUp is an upstream that answers every request with a summary and
// keeps every body it was sent, for the tests of a provider's own body
// limit (body_limit.go).
type limitedUp struct {
	proto provider.Protocol
	mu    sync.Mutex
	got   [][]byte
}

func (u *limitedUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.got = append(u.got, b)
	u.mu.Unlock()
	if u.proto == provider.Chat {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"SUMMARY"}}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`,
			`data: [DONE]`))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, sse(
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"SUMMARY"}]}}`,
		`data: {"type":"response.completed","response":{"id":"resp_s","status":"completed","output":[]}}`))
}

func (u *limitedUp) calls() [][]byte {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([][]byte(nil), u.got...)
}

// limitOn points magpie's provider file at a temp dir and adds provider
// "fake" on up, whose body limit is n bytes (0: none, as it is by
// default).
func limitOn(t *testing.T, up *limitedUp, n int) {
	t.Helper()
	noRests()
	t.Cleanup(noRests)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	host := httptest.NewServer(up)
	t.Cleanup(host.Close)
	p := provider.Provider{ID: "fake", Name: "1017dp", Key: "k", Models: []string{"m1"}, BodyLimit: n}
	if up.proto == provider.Chat {
		p.Chat = host.URL + "/v1"
	} else {
		p.Responses = host.URL + "/v1"
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

// longTurn is a Codex turn whose body carries a plain Chinese text well
// over a MiB: UTF-8 counted in bytes, as a vendor counts them.
func longTurn(fill int) string {
	return `{"model":"fake/m1","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"请把这整段记录压缩成一句话 ` + strings.Repeat("记录", fill) + `"}]}]}`
}

// A provider's own body limit refuses an over-the-limit request here, a
// 413 that says the size and the limit: the upstream is asked nothing,
// and nothing is sent again in its place (issue #4282). The limit counts
// bytes, a Chinese text among them.
func TestBodyLimitRefusedUnsent(t *testing.T) {
	up := &limitedUp{proto: provider.Responses}
	limitOn(t, up, 1<<20)
	body := longTurn(300_000)
	if len(body) <= 1<<20 {
		t.Fatalf("the test body is only %d bytes", len(body))
	}
	code, said := post(t, "/v1/responses", body)
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("answered %d: %.400s", code, said)
	}
	if !strings.Contains(said, "request_too_large") || !strings.Contains(said, "body limit") {
		t.Errorf("the refusal doesn't say it is the body limit: %.400s", said)
	}
	if n := len(up.calls()); n != 0 {
		t.Fatalf("the upstream was asked %d times", n)
	}
}

// With no body limit set — as it is by default — the same body goes to
// the provider as it always did.
func TestBodyLimitOffByDefault(t *testing.T) {
	up := &limitedUp{proto: provider.Responses}
	limitOn(t, up, 0)
	code, said := post(t, "/v1/responses", longTurn(300_000))
	if code != 200 {
		t.Fatalf("answered %d: %.400s", code, said)
	}
	if n := len(up.calls()); n != 1 {
		t.Fatalf("the upstream was asked %d times", n)
	}
}

// The limit counts the request's final body, as it goes out after the
// protocol's conversion (an Anthropic client asked as Chat), not the body
// the client sent; a body that comes to exactly the limit goes.
func TestBodyLimitCountsTheBodyAsSent(t *testing.T) {
	// as it goes out, with no limit: the converted body's size, which a
	// conversion may only grow
	up := &limitedUp{proto: provider.Chat}
	limitOn(t, up, 0)
	body := `{"model":"fake/m1","max_tokens":16,"messages":[{"role":"user","content":"请把这段记录压缩成一句话 ` + strings.Repeat("记录", 60_000) + `"}]}`
	sizes := map[int]bool{}
	for range 2 {
		code, said := postAnthropic(t, body)
		if code != 200 {
			t.Fatalf("answered %d: %.400s", code, said)
		}
		calls := up.calls()
		sizes[len(calls[len(calls)-1])] = true
	}
	if len(sizes) != 1 {
		t.Fatalf("the converted body came out %v over two asks", sizes)
	}
	sent := len(up.calls()[0])
	if inbound := len(body); inbound >= sent-1 {
		t.Fatalf("the conversion didn't grow the body: %d in, %d out", inbound, sent)
	}

	// a byte under what went out: refused here, the upstream asked nothing
	up2 := &limitedUp{proto: provider.Chat}
	limitOn(t, up2, sent-1)
	if code, said := postAnthropic(t, body); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a byte under the size it goes out at answered %d: %.400s", code, said)
	}
	if n := len(up2.calls()); n != 0 {
		t.Fatalf("the upstream was asked %d times", n)
	}

	// the size it goes out at, exactly: it goes
	up3 := &limitedUp{proto: provider.Chat}
	limitOn(t, up3, sent)
	if code, said := postAnthropic(t, body); code != 200 {
		t.Fatalf("at the size it goes out at exactly, answered %d: %.400s", code, said)
	}
	if n := len(up3.calls()); n != 1 {
		t.Fatalf("the upstream was asked %d times", n)
	}
}

// The refusal is not handed to the provider's fallback: the limit is the
// user's own word for this provider, the body never went out, and no
// other provider is asked for it — nothing is retried, nothing switches
// (issue #4282). A vendor's own 413 hands over, as it always did; this
// one was never the vendor's word.
func TestBodyLimitRefusalNotHandedOver(t *testing.T) {
	upA := &limitedUp{proto: provider.Responses}
	upB := &limitedUp{proto: provider.Responses}
	noRests()
	t.Cleanup(noRests)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	hostA := httptest.NewServer(upA)
	hostB := httptest.NewServer(upB)
	t.Cleanup(func() { hostA.Close(); hostB.Close() })
	p := provider.Provider{ID: "fake", Name: "1017dp", Key: "k", Models: []string{"m1"}, BodyLimit: 1 << 20, Fallback: []string{"fake2/m1"}, Responses: hostA.URL + "/v1"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	p2 := provider.Provider{ID: "fake2", Name: "1017dp2", Key: "k", Models: []string{"m1"}, Responses: hostB.URL + "/v1"}
	if err := provider.Save(p2); err != nil {
		t.Fatal(err)
	}
	code, said := post(t, "/v1/responses", longTurn(300_000))
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("answered %d: %.400s", code, said)
	}
	if !strings.Contains(said, "body limit") {
		t.Errorf("the refusal doesn't say it is the body limit: %.400s", said)
	}
	if n := len(upA.calls()); n != 0 {
		t.Fatalf("the limited provider was asked %d times", n)
	}
	if n := len(upB.calls()); n != 0 {
		t.Fatalf("the fallback was asked for a body the limit refused, %d times, and answered it", n)
	}
}

// postAnthropic asks the gateway's /v1/messages as Claude Code does.
func postAnthropic(t *testing.T, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	New().Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}
