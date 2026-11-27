package gateway

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// deepseekBodyCap is what DeepSeek's API answered a Codex user's long
// session with (celestial_eagle_99230 on Discord: "1017dp: 413 Request
// Entity Too Large: Failed to buffer the request body: length limit
// exceeded"): axum's text for a body past its byte limit, as text/plain.
// It is a cap on the request's bytes, which DeepSeek documents nowhere;
// its 1M-token window is not what was full.
const deepseekBodyCap = "Failed to buffer the request body: length limit exceeded"

// deepseekOverflow is DeepSeek's own 400 for a prompt past the model's
// window, OpenAI's words.
const deepseekOverflow = `{"error":{"message":"This model's maximum context length is 1048576 tokens. However, you requested 1100000 tokens (1100000 in the messages, 0 in the completion). Please reduce the length of the messages or completion.","type":"invalid_request_error","param":null,"code":"invalid_request_error"}}`

// capped is an upstream that refuses a body of more than most bytes with
// status and body (as ctype), and answers any other with a summary in its
// protocol's stream. Every body it is sent is kept.
type capped struct {
	proto  provider.Protocol
	most   int
	status int
	ctype  string
	body   string
	mu     sync.Mutex
	got    [][]byte
}

func (c *capped) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	c.got = append(c.got, b)
	c.mu.Unlock()
	if len(b) > c.most {
		w.Header().Set("Content-Type", c.ctype)
		w.WriteHeader(c.status)
		io.WriteString(w, c.body)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	if c.proto == provider.Chat {
		io.WriteString(w, sse(
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"SUMMARY"}}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`,
			`data: [DONE]`))
		return
	}
	io.WriteString(w, sse(
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"SUMMARY"}]}}`,
		`data: {"type":"response.completed","response":{"id":"resp_s","status":"completed","output":[]}}`))
}

func (c *capped) calls() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]byte(nil), c.got...)
}

// cappedOn saves provider "fake" (model m1) on c, as setup does.
func cappedOn(t *testing.T, c *capped) {
	t.Helper()
	noRests()
	t.Cleanup(noRests)
	// The hold before routing is not what these tests are about, and their
	// bodies are up to 42 MB: magpie's work on one before its first route
	// is ~0.6s, ~9s under -race on a loaded Mac, and past unroutedUnknown's
	// 30s on a loaded ubuntu runner under -race, which answered the
	// compaction 504 "held 30s without routing" (#5571). The hold's own
	// tests are in unrouted_test.go.
	unknownLimit(t, 10*time.Minute)
	unroutedLimits(t, unroutedShow, 10*time.Minute)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(c)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "fake", Name: "1017dp", Key: "k", Models: []string{"m1"}}
	if c.proto == provider.Chat {
		p.Chat = up.URL + "/v1"
	} else {
		p.Responses = up.URL + "/v1"
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

// bigCompaction is Codex's summary request (remote compaction v2: a
// /responses stream ending in compaction_trigger) of a session holding a
// pasted screenshot as base64 and a long tool output: well over the cap
// below, while the conversation as text is well under it.
func bigCompaction() string {
	img := strings.Repeat("iVBORw0KGgo", 60_000) // ~660KB of base64
	out := strings.Repeat("log line\\n", 40_000) // ~400KB of tool output
	return `{"model":"fake/m1","stream":true,"store":false,"prompt_cache_key":"thread-ds","include":["reasoning.encrypted_content"],
  "input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the login bug"},{"type":"input_image","image_url":"data:image/png;base64,` + img + `"}]},
  {"type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"cmd\":\"cat build.log\"}"},
  {"type":"function_call_output","call_id":"call_1","output":"` + out + `"},
  {"type":"message","role":"assistant","content":[{"type":"output_text","text":"I fixed main.go"}]},
  {"type":"message","role":"user","content":[{"type":"input_text","text":"now add a test"}]},
  {"type":"compaction_trigger"}]}`
}

const cappedMost = 256 << 10

// A summary request too large for the vendor — past DeepSeek's byte cap
// (413), or past the model's window — went back to Codex as that
// failure. Codex's remote compaction has no way to shrink it (it trims
// only by its token estimate against the catalog's 1M window, and asks
// nothing smaller on context_length_exceeded), so every compaction failed
// the same way and the session could neither compact nor go on. magpie
// asks again with the conversation as text, its images left out and its
// long outputs cut, and Codex installs the model's summary.
func TestCodexCompactTooLargeAsksSmaller(t *testing.T) {
	for _, proto := range []provider.Protocol{provider.Chat, provider.Responses} {
		for _, c := range []struct {
			name, ctype, body string
			status            int
		}{
			{"byte cap", "text/plain; charset=utf-8", deepseekBodyCap, 413},
			{"overflow", "application/json", deepseekOverflow, 400},
		} {
			t.Run(string(proto)+" "+c.name, func(t *testing.T) {
				up := &capped{proto: proto, most: cappedMost, status: c.status, ctype: c.ctype, body: c.body}
				cappedOn(t, up)
				code, body := codexPost(t, bigCompaction())
				if code != 200 {
					t.Fatalf("Codex's compaction failed: %d %s", code, body)
				}
				if got := compactedText(t, body); got != "SUMMARY" {
					t.Fatalf("summary %q", got)
				}
				calls := up.calls()
				// the last ask is the smaller one (a Chat overflow is
				// asked once more by serve itself before)
				if len(calls) < 2 || len(calls[0]) <= cappedMost {
					t.Fatalf("asked %d times", len(calls))
				}
				small := string(calls[len(calls)-1])
				if len(small) > cappedMost || strings.Contains(small, "iVBORw0KGgo") {
					t.Fatalf("asked again as large (%d bytes) or with the image", len(small))
				}
				for _, want := range []string{"fix the login bug", "now add a test", "I fixed main.go"} {
					if !strings.Contains(small, want) {
						t.Errorf("the smaller ask lacks %q", want)
					}
				}
			})
		}
	}
}

// When the conversation as text is refused too, magpie compacts without a
// model, so Codex still goes on.
func TestCodexCompactTooLargeCompactsLocally(t *testing.T) {
	up := &capped{proto: provider.Chat, most: 0, status: 413, ctype: "text/plain; charset=utf-8", body: deepseekBodyCap}
	cappedOn(t, up)
	code, body := codexPost(t, bigCompaction())
	if code != 200 {
		t.Fatalf("Codex's compaction failed: %d %s", code, body)
	}
	got := compactedText(t, body)
	for _, want := range []string{"fix the login bug", "now add a test", "I fixed main.go", "413", "length limit exceeded"} {
		if !strings.Contains(got, want) {
			t.Errorf("local summary lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "iVBORw0KGgo") {
		t.Error("local summary holds the image")
	}
}

// A refusal of the summary request that says nothing of its size goes
// back to Codex as before, not asked again as text: only a request too large is asked
// again smaller.
func TestCodexCompactOtherRefusalStays(t *testing.T) {
	up := &capped{proto: provider.Chat, most: 0, status: 400, ctype: "application/json",
		body: `{"error":{"message":"Invalid tool schema","type":"invalid_request_error"}}`}
	cappedOn(t, up)
	code, body := codexPost(t, bigCompaction())
	if code != 400 || !strings.Contains(body, "Invalid tool schema") {
		t.Fatalf("%d %s", code, body)
	}
	for _, b := range up.calls() {
		if strings.Contains(string(b), "This is the conversation so far") {
			t.Fatalf("asked again as text: %.200s", b)
		}
	}
}

// The turn itself, refused with DeepSeek's byte cap before Codex's stream
// began, reaches Codex as response.failed context_length_exceeded, the
// code it compacts on; another client keeps the 413.
func TestDeepSeekByteCapTurn(t *testing.T) {
	for _, proto := range []provider.Protocol{provider.Chat, provider.Responses} {
		t.Run(string(proto), func(t *testing.T) {
			fresh(t)
			up := &capped{proto: proto, most: 0, status: 413, ctype: "text/plain; charset=utf-8", body: deepseekBodyCap}
			cappedOn(t, up)
			code, ctype, body := askCodexStream(t, New(), "fake/m1", codexUA, nil)
			if code != 200 || !strings.HasPrefix(ctype, "text/event-stream") || failedCode(body) != "context_length_exceeded" {
				t.Fatalf("Codex wasn't told to compact: %d %s %s", code, ctype, body)
			}
			code, _, body = askCodexStream(t, New(), "fake/m1", "opencode/1.4.0 ai-sdk/openai", nil)
			if code != 413 || !strings.Contains(body, "request_too_large") {
				t.Fatalf("another client's 413: %d %s", code, body)
			}
		})
	}
}

// inStreamOn saves provider "fake" (model m1, Responses) on f, as cappedOn
// does.
func inStreamOn(t *testing.T, f *inStream) {
	t.Helper()
	noRests()
	t.Cleanup(noRests)
	unknownLimit(t, 10*time.Minute)
	unroutedLimits(t, unroutedShow, 10*time.Minute)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "fake", Name: "1017dp", Key: "k", Models: []string{"m1"}}
	p.Responses = up.URL + "/v1"
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

// compactionOverflow is the member's overflow refusal 1007cordon-ux
// reproduced (#5134): a summary request for a long thread refused in the
// reply's own stream, a response.failed, with no status saying 4xx.
const compactionOverflow = "Your input exceeds the context window of this model. Please adjust your input and try again."

// inStream refuses the summary ask as its own stream's response.failed
// saying why (message): what the recorder holds when the last candidate
// left began a 200 stream and failed inside it. The conversation as text
// is answered with a summary at once.
type inStream struct {
	message string
	mu      sync.Mutex
	got     [][]byte
}

func (f *inStream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.got = append(f.got, b)
	f.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	if bytes.Contains(b, []byte("This is the conversation so far")) {
		io.WriteString(w, sse(
			`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"SUMMARY"}]}}`,
			`data: {"type":"response.completed","response":{"id":"resp_s","status":"completed","output":[]}}`))
		return
	}
	io.WriteString(w, sse(
		`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"resp_f","status":"in_progress"}}`,
		`event: response.failed`+"\n"+`data: {"type":"response.failed","response":{"id":"resp_f","status":"failed","error":{"code":"invalid_request_error","message":"`+f.message+`"}}}`))
}

func (f *inStream) calls() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.got...)
}

// A summary ask refused for the thread's size in its own stream — a
// response.failed, the recorder's status the 200 its headers began with —
// was no cue to ask the conversation as text: summaryTooLarge reads the
// status alone, and the overflow's words match no predicate of the
// in-stream refusals asked again (#5134, Kesuan; 1007cordon-ux: an
// overflow never succeeds on retry, so the thread sat in compactFails for
// good). The message alone asks the summary again as text.
func TestCodexCompactOverflowInStreamAsksAsText(t *testing.T) {
	up := &inStream{message: compactionOverflow}
	inStreamOn(t, up)
	code, body := codexPost(t, bigCompaction())
	if code != 200 {
		t.Fatalf("Codex's compaction failed: %d %s", code, body)
	}
	if got := compactedText(t, body); got != "SUMMARY" {
		t.Fatalf("summary %q", got)
	}
	calls := up.calls()
	if len(calls) < 2 || !strings.Contains(string(calls[len(calls)-1]), "This is the conversation so far") {
		t.Fatalf("asked %d times, the last not as text", len(calls))
	}
}

// An in-stream refusal that says nothing of the thread's size is no cue
// to ask as text, as before: only an overflow is asked again smaller.
func TestCodexCompactInStreamOtherRefusalStays(t *testing.T) {
	up := &inStream{message: "Invalid tool schema"}
	inStreamOn(t, up)
	code, body := codexPost(t, bigCompaction())
	if code != 502 || !strings.Contains(body, "Invalid tool schema") {
		t.Fatalf("%d %s", code, body)
	}
	for _, b := range up.calls() {
		if strings.Contains(string(b), "This is the conversation so far") {
			t.Fatalf("asked again as text: %.200s", b)
		}
	}
}
