package gateway

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// A raw control byte inside a JSON string is what JSON forbids, and \u00XX
// is the same character in the only spelling the grammar takes: the turn is
// escaped and served, not refused (#5668). The reporter's shape: a long
// thread whose text carries a raw NUL, on Codex's own backend path, where
// the refusal was magpie's own 400 with no usage row written.
func TestRawControlByteInStringEscapedAndRouted(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	var got []byte
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.Header()["Content-Type"] = nil // as the ChatGPT backend sends it
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1"}}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":9,"output_tokens":2}}}`))
	})
	text := strings.Repeat("x", 6_800_000) // as long a turn as the reporter's
	body := `{"model":"gpt-5.5","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"` + text + "\x00end\"}]}]}"
	code, reply := codexPost(t, body)
	if code != 200 {
		t.Fatalf("status %d: %s", code, reply)
	}
	if i := bytes.IndexByte(got, 0); i >= 0 {
		t.Fatalf("upstream got the raw NUL still in place, first at %d", i)
	}
	if !bytes.Contains(got, []byte(`x\u0000end`)) {
		t.Errorf("upstream body lacks the escaped NUL (len %d)", len(got))
	}
	// the turn served is a turn counted: the refusal the issue reports
	// writes no usage row, this does
	if u := usage.Load(time.Time{}); len(u) != 1 || u[0].Input != 9 || u[0].Output != 2 {
		t.Errorf("usage %+v", u)
	}
}

// The same byte on a magpie model's own route, and the one line the issue
// asks be logged: how many, the offset of the first, an excerpt.
func TestRawControlByteInStringEscapedOnResponses(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"response.created","response":{"id":"r1","model":"m1"}}`,
		`data: {"type":"response.completed","response":{"id":"r1","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`)}
	setup(t, provider.Responses, f)
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	code, reply := post(t, "/v1/responses", `{"model":"fake/m1","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"tab`+"\x01"+`stop"}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, reply)
	}
	if i := bytes.IndexByte(f.got, 1); i >= 0 {
		t.Fatalf("upstream got the raw \\x01 still in place, first at %d", i)
	}
	if !bytes.Contains(f.got, []byte(`tab\u0001stop`)) {
		t.Errorf("upstream body lacks the escaped byte: %.120q", f.got)
	}
	if line := logs.String(); !strings.Contains(line, "escaped 1 raw control byte") ||
		!strings.Contains(line, "first at offset") {
		t.Errorf("log: %q", line)
	}
}

// A raw control byte outside a string is a broken structure escaping cannot
// mend: refused as before, with the first parse's error, and the upstream
// never asked.
func TestRawControlByteOutsideStringStillRefused(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":1,"output_tokens":1}}}`)}
	setup(t, provider.Responses, f)
	code, reply := post(t, "/v1/responses", "{\x00\"model\":\"fake/m1\",\"input\":\"hi\"}")
	if code != 400 || !strings.Contains(reply, "invalid character") {
		t.Fatalf("status %d: %s", code, reply)
	}
	if f.calls != 0 {
		t.Errorf("upstream asked %d times", f.calls)
	}
}

// A body that parses is walked never and logged never: the fallback costs
// a valid request one failed parse, nothing more (#5668's performance
// note). Byte for byte, the body served is the body read.
func TestParsedRequestEscapesNothing(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"response.created","response":{"id":"r1","model":"m1"}}`,
		`data: {"type":"response.completed","response":{"id":"r1","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`)}
	setup(t, provider.Responses, f)
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	code, reply := post(t, "/v1/responses", `{"model":"fake/m1","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"line\nbreak\ttab \\ quoted \" end"}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, reply)
	}
	if line := logs.String(); strings.Contains(line, "escaped") {
		t.Errorf("logged for a body that parses: %q", line)
	}
	if !bytes.Contains(f.got, []byte(`line\nbreak\ttab \\ quoted \" end`)) {
		t.Errorf("upstream body lost the text as sent: %.200q", f.got)
	}
}

func TestEscapeRawControlBytes(t *testing.T) {
	for _, c := range []struct {
		name, in, want string // want "" says: returned as it came
		n, off         int
	}{
		{"in a string", "{\"a\":\"x\x01y\"}", "{\"a\":\"x\\u0001y\"}", 1, 7},
		{"whitespace between tokens stays", "{\n\"a\":\n1}", "", 0, -1},
		{"an escaped newline stays", `{"a":"x\ny"}`, "", 0, -1},
		{"past an escaped quote", "{\"a\":\"x\\\"y\x00z\"}", "{\"a\":\"x\\\"y\\u0000z\"}", 1, 10},
		{"past an escaped backslash", "{\"a\":\"x\\\\\x00\"}", "{\"a\":\"x\\\\\\u0000\"}", 1, 9},
		{"outside a string stays", "{\"a\":\"ok\"}\x00", "", 0, -1},
		{"two strings, one byte each", "{\"a\":\"\x02\",\"b\":\"\x1f\"}", "{\"a\":\"\\u0002\",\"b\":\"\\u001f\"}", 2, 6},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, n, off := escapeRawControlBytes([]byte(c.in))
			want := c.want
			if want == "" {
				want = c.in
			}
			if string(got) != want || n != c.n || off != c.off {
				t.Fatalf("got %q n=%d off=%d, want %q n=%d off=%d", got, n, off, want, c.n, c.off)
			}
		})
	}
}

// A body that parses comes back the very bytes it came as, the whitespace
// around it included: the fallback owns only what it mended.
func TestDecodeRequestReturnsTheBodyItTook(t *testing.T) {
	body := []byte("  {\"model\":\"m1\"}\n\t")
	var q struct {
		Model string `json:"model"`
	}
	fixed, err := decodeRequest(body, &q)
	if err != nil {
		t.Fatalf("decodeRequest: %v", err)
	}
	if q.Model != "m1" {
		t.Errorf("model %q", q.Model)
	}
	if len(fixed) != len(body) || &fixed[0] != &body[0] {
		t.Errorf("a parsed body was rewritten: %q", fixed)
	}
}

// The mended body is what comes back to be routed: an escape the client
// never sent is what the upstream would refuse, so the repaired copy, not
// the raw one, goes on.
func TestDecodeRequestEscapesAndReturnsTheMendedBody(t *testing.T) {
	var q struct {
		Model string `json:"model"`
	}
	fixed, err := decodeRequest([]byte("{\"model\":\"m1\",\"note\":\"a\x00b\"}"), &q)
	if err != nil {
		t.Fatalf("decodeRequest: %v", err)
	}
	if q.Model != "m1" {
		t.Errorf("model %q", q.Model)
	}
	if string(fixed) != `{"model":"m1","note":"a\u0000b"}` {
		t.Errorf("got %q", fixed)
	}
}

// A body the mended copy does not parse either is refused with the first
// parse's error, not the repair's: the client is told of the body it sent.
func TestEscapeFallbackKeepsTheOriginalError(t *testing.T) {
	_, err := decodeRequest([]byte("{\"model\":\"m1\",\"a\":\"x\x00y\",\"b\":}"), &struct{}{})
	if err == nil {
		t.Fatal("decodeRequest accepted a body broken past its control byte")
	}
	if !strings.Contains(err.Error(), `invalid character '\x00' in string literal`) {
		t.Errorf("error %q, want the first parse's", err)
	}
	if strings.Contains(err.Error(), "'}'") || strings.Contains(err.Error(), "looking for beginning of value") {
		t.Errorf("error %q names the repair's parse, not the body's", err)
	}
}

// Ollama's door reads the request itself (ollamaChat), and what it routes
// is the rebuild of what it decoded: a raw control byte in a message goes
// out escaped there too (#5668).
func TestOllamaChatRawControlByteInStringEscaped(t *testing.T) {
	fresh(t)
	var got []string
	ollamaPlan(t, ollamaUpstream(t, &got).URL+"/v1")
	h := lanGuard(New().Handler())
	w := ollamaCall(t, h, "/api/chat", `{"model":"plan/seer","messages":[{"role":"user","content":"tab`+"\x01"+`stop"}],"stream":false}`)
	if w.Code != 200 || len(got) != 1 {
		t.Fatalf("chat: %d %s (upstream %d)", w.Code, w.Body, len(got))
	}
	if strings.Contains(got[0], "\x01") {
		t.Errorf("upstream got the raw byte: %s", got[0])
	}
	if !strings.Contains(got[0], `tab\u0001stop`) {
		t.Errorf("upstream body lacks the escaped byte: %s", got[0])
	}
}
