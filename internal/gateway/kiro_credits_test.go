package gateway

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The Kiro plugin's replies, once it sums the meteringEvents Kiro costs
// its requests with ({"unit":"credit","usage":<n>}): the last
// message_delta's usage names the request's credits beside
// magpie_cache_unknown, as the issue asks it to (yetone/magpie#4328).
// The Usage page showed no credits on a Kiro row: aUsage read no credit,
// though the Chat path has read WorkBuddy's since #2512.
const (
	kiroCreditsStream = "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_k1","type":"message","role":"assistant","model":"claude-sonnet-4.5","content":[],"usage":{"input_tokens":0,"output_tokens":0}}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"OK"}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":0,"output_tokens":19,"magpie_cache_unknown":true,"credit":1.25}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
	kiroCreditsJSON = `{"id":"msg_k2","type":"message","role":"assistant","model":"claude-sonnet-4.5","content":[{"type":"text","text":"OK."}],"stop_reason":"end_turn","usage":{"input_tokens":0,"output_tokens":1,"magpie_cache_unknown":true,"credit":0.4}}`
	kiroFreeCredits = `{"id":"msg_k3","type":"message","role":"assistant","model":"claude-sonnet-4.5","content":[{"type":"text","text":"OK."}],"stop_reason":"end_turn","usage":{"input_tokens":0,"output_tokens":1,"magpie_cache_unknown":true,"credit":0}}`
	kiroNoCredits   = `{"id":"msg_k4","type":"message","role":"assistant","model":"claude-sonnet-4.5","content":[{"type":"text","text":"OK."}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	kiroBadCredits  = `{"id":"msg_k5","type":"message","role":"assistant","model":"claude-sonnet-4.5","content":[{"type":"text","text":"OK."}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1,"credit":-3}}`
)

// TestKiroCreditsReachTheLedger: the credits a reply on the Messages path
// names reach the ledger's record, as WorkBuddy's do on the Chat path
// (#2512). A free request's credit 0 is a said 0, not an unknown one, and
// a credit no reply said — or a negative one, which is no cost — leaves
// the row uncredited.
func TestKiroCreditsReachTheLedger(t *testing.T) {
	const messages = `{"model":"fake/m1","max_tokens":20,"messages":[{"role":"user","content":"Say just OK."}]}`
	const streamed = `{"model":"fake/m1","max_tokens":20,"stream":true,"messages":[{"role":"user","content":"Say just OK."}]}`
	const chat = `{"model":"fake/m1","messages":[{"role":"user","content":"Say just OK."}]}`
	for _, c := range []struct {
		name, ctype, reply, path, request string
		want                              *float64
	}{
		{"whole reply", "application/json", kiroCreditsJSON, "/v1/messages", messages, ptr(0.4)},
		{"streamed", "", kiroCreditsStream, "/v1/messages", streamed, ptr(1.25)},
		// a translated request is always streamed from the vendor
		{"translated for a Chat client", "", kiroCreditsStream, "/v1/chat/completions", chat, ptr(1.25)},
		{"a free request", "application/json", kiroFreeCredits, "/v1/messages", messages, ptr(0.0)},
		{"no credit said", "application/json", kiroNoCredits, "/v1/messages", messages, nil},
		{"a negative credit", "application/json", kiroBadCredits, "/v1/messages", messages, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			setup(t, provider.Anthropic, &fake{ctype: c.ctype, reply: c.reply})
			if code, body := post(t, c.path, c.request); code != 200 {
				t.Fatalf("gateway: %d %s", code, body)
			}
			r := lastUsage(t)
			switch {
			case c.want == nil && r.Credits != nil:
				t.Fatalf("credits %v from a reply that said none", *r.Credits)
			case c.want != nil && (r.Credits == nil || *r.Credits != *c.want):
				t.Fatalf("credits %v, want %v", r.Credits, *c.want)
			}
		})
	}
}
