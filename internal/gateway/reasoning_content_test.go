package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #2855: a Codex conversation that used OpenAI, then went through magpie to
// DeepSeek, then had magpie disconnected again, was stuck on HTTP 400
// "Invalid 'input[43].content': array too long. Expected an array with
// maximum length 0, but got an array with length 1 instead." — the remote
// compact with it. The reporter's rollout holds the poison: 25 reasoning
// items the DeepSeek phase handed Codex, each with its text in content
// parts and a 38-character encrypted_content of the vendor's own (not the
// gAAAA its GPT-phase items carry), which Codex stores and replays to
// OpenAI when the thread goes back there. OpenAI takes a reasoning item
// sent back only with its content empty (#1411) and cannot open another
// vendor's seal. So what magpie hands a client must be re-sendable to a
// strict Responses upstream: a reasoning item magpie makes, its text a
// summary, nothing sealed — the shape its own translations have always
// given, which the first test pins.

// dskItem is such an item, as the reporter's rollout holds it: the text
// where OpenAI wants none, a summary empty, and a seal of the vendor's own.
const dskItem = `{"type":"reasoning","id":"rs_dsk1","summary":[],"content":[{"type":"reasoning_text","text":"counting the lines"}],"encrypted_content":"dskseal0000000000000000000000000000000"}`

// dskRelay is a Responses upstream serving a DeepSeek model as a relay in
// front of one does: its reasoning item carries the model's text in it and
// a seal of its own, beside an answer.
type dskRelay struct{}

func (u *dskRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/models") {
		io.WriteString(w, `{"models":[{"slug":"deepseek-flash","visibility":"list"}]}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, sse(
		`data: {"type":"response.created","response":{"id":"resp_dsk","model":"deepseek-flash","status":"in_progress"}}`,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_dsk1","summary":[]}}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":`+dskItem+`}`,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"msg1","role":"assistant","content":[]}}`,
		`data: {"type":"response.output_text.delta","item_id":"msg1","output_index":1,"delta":"42 lines"}`,
		`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"message","id":"msg1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"42 lines"}]}}`,
		`data: {"type":"response.completed","response":{"id":"resp_dsk","status":"completed","output":[`+dskItem+`,{"type":"message","id":"msg1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"42 lines"}]}],"usage":{"input_tokens":5,"output_tokens":2}}}`))
}

func saveDSK(t *testing.T, u http.Handler) {
	t.Helper()
	srv := httptest.NewServer(u)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "dsk", Name: "DSK", Key: "k", Models: []string{"deepseek-flash"}, Responses: srv.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
}

// clientReasoning reads a client-side stream back: every reasoning item of
// an output_item.done, and every one response.completed's output carries.
func clientReasoning(t *testing.T, body string) (done, completed []map[string]any) {
	t.Helper()
	for _, ln := range strings.Split(body, "\n") {
		data, ok := strings.CutPrefix(strings.TrimSpace(ln), "data: ")
		if !ok || !strings.Contains(data, `"reasoning"`) {
			continue
		}
		var ev struct {
			Type     string         `json:"type"`
			Item     map[string]any `json:"item"`
			Response struct {
				Output []map[string]any `json:"output"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "response.output_item.done":
			if ev.Item["type"] == "reasoning" {
				done = append(done, ev.Item)
			}
		case "response.completed", "response.incomplete":
			for _, it := range ev.Response.Output {
				if it["type"] == "reasoning" {
					completed = append(completed, it)
				}
			}
		}
	}
	return done, completed
}

// assertResendable is a reasoning item a client may store and replay where
// OpenAI's rules hold: no content, no seal but OpenAI's own — none here —
// and its reasoning as a summary.
func assertResendable(t *testing.T, it map[string]any, text string) {
	t.Helper()
	if c, ok := it["content"]; ok {
		if parts, _ := c.([]any); len(parts) > 0 {
			t.Errorf("reasoning item carries its text: %v", it)
		}
	}
	if enc, _ := it["encrypted_content"].(string); enc != "" {
		t.Errorf("reasoning item carries a seal that is nobody's here: %q", enc)
	}
	sum, _ := it["summary"].([]any)
	if len(sum) == 0 {
		t.Errorf("reasoning item carries no summary: %v", it)
		return
	}
	p0, _ := sum[0].(map[string]any)
	if s, _ := p0["text"].(string); s != text {
		t.Errorf("summary text %q, want the item's reasoning %q", s, text)
	}
}

// A Chat upstream's reasoning, translated for Codex, is an item magpie
// makes: an rs_ id and a summary, no content, nothing sealed. It has been
// so on main; this pins the contract #2855 turns on.
func TestTranslatedChatReasoningCarriesNoContent(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			io.WriteString(w, `{"models":[{"slug":"deepseek-v4.1-flash","visibility":"list"}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"","reasoning_content":"weighing the options"}}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"42 lines"}}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`data: [DONE]`))
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "dsh", Name: "DSH", Key: "k", Chat: up.URL + "/v1", Models: []string{"deepseek-v4.1-flash"}}); err != nil {
		t.Fatal(err)
	}
	body := `{"model":"dsh/deepseek-v4.1-flash","stream":true,"store":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"count the lines"}]}]}`
	rec := codexPostTo(New(), body)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "42 lines") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	done, completed := clientReasoning(t, rec.Body.String())
	if len(done) != 1 || len(completed) != 1 {
		t.Fatalf("%d reasoning items done, %d in the completed reply: %s", len(done), len(completed), rec.Body)
	}
	for _, it := range append(done, completed...) {
		if id, _ := it["id"].(string); !strings.HasPrefix(id, "rs_") {
			t.Errorf("reasoning id %q, want one of magpie's own", id)
		}
		assertResendable(t, it, "weighing the options")
	}
}

// A Responses upstream's reasoning item with its text in it, served
// translated (Codex asked a search the model doesn't run itself), goes to
// Codex as the upstream wrote it today: stored, and poison when the thread
// replays to OpenAI, on magpie or off it (#2855). It goes as an item
// magpie makes, its text a summary, nothing sealed.
func TestForeignSealedReasoningGivenCodexCarriesNoContent(t *testing.T) {
	fresh(t)
	saveDSK(t, &dskRelay{})
	body := `{"model":"dsk/deepseek-flash","stream":true,"store":false,"tools":[{"type":"web_search"}],"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"count the lines"}]}]}`
	rec := codexPostTo(New(), body)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "42 lines") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	done, completed := clientReasoning(t, rec.Body.String())
	if len(done) != 1 || len(completed) != 1 {
		t.Fatalf("%d reasoning items done, %d in the completed reply: %s", len(done), len(completed), rec.Body)
	}
	for _, it := range append(done, completed...) {
		if id, _ := it["id"].(string); id != "rs_dsk1" {
			t.Errorf("reasoning id %q, want the item's own, its events named by it", id)
		}
		assertResendable(t, it, "counting the lines")
	}
}

// The same item in a reply relayed as it came — a plain turn to a provider
// serving the model on Responses — which is how a DeepSeek phase usually
// reaches Codex: the bytes alone would leave the poison in Codex's rollout.
// They go through the tidy the translated reply's items do.
func TestForeignSealedReasoningRelayedCarriesNoContent(t *testing.T) {
	fresh(t)
	saveDSK(t, &dskRelay{})
	body := `{"model":"dsk/deepseek-flash","stream":true,"store":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"count the lines"}]}]}`
	rec := codexPostTo(New(), body)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "42 lines") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "sequence_number") {
		t.Fatalf("the reply was translated, not relayed: %s", rec.Body)
	}
	done, completed := clientReasoning(t, rec.Body.String())
	if len(done) != 1 || len(completed) != 1 {
		t.Fatalf("%d reasoning items done, %d in the completed reply: %s", len(done), len(completed), rec.Body)
	}
	for _, it := range append(done, completed...) {
		assertResendable(t, it, "counting the lines")
	}
}
