package gateway

import (
	"net/http"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #7707 (devin-auth): Anthropic policy refusals a plugin forwards as plain
// error messages, with no structured code beside them — a 400
// invalid_request_error, a 502 devin_error whose code is a number, an
// api_error in a stream's error event — none of which carries a
// filterReasons code. The messages below are the reporter's, word for
// word; the vendor's own words are the only thing that tells them from
// any other 400.
const (
	cyberPolicyMessage   = "This request was flagged by Anthropic's cyber verification policy. You can enroll in the Anthropic Cyber Verification Program to get access to less restrictive content policies."
	contentPolicyMessage = "This request was refused by Anthropic's content policy. Please retry with a different model."
)

func TestAnthropicPlainTextPolicyRefusal(t *testing.T) {
	for _, body := range []string{
		`{"type":"error","error":{"type":"invalid_request_error","message":"` + cyberPolicyMessage + `"}}`,
		`{"type":"error","error":{"type":"devin_error","code":502,"message":"` + cyberPolicyMessage + `"}}`,
		`{"type":"error","error":{"type":"api_error","message":"` + cyberPolicyMessage + `"}}`,
		`{"type":"error","error":{"type":"invalid_request_error","message":"` + contentPolicyMessage + `"}}`,
	} {
		note, ok := policyRefusal([]byte(body))
		if !ok {
			t.Errorf("not classified as a refusal: %s", body)
			continue
		}
		want := "cyber_policy"
		if strings.Contains(body, "content policy. Please") {
			want = "content_policy"
		}
		if !strings.Contains(note, want) || !strings.Contains(note, "Anthropic") {
			t.Errorf("%s: note %q, want it to name %s and keep the vendor's words", body, note, want)
		}
	}
	// an error event of the same shapes, no code beside it: refused, not
	// a failure the account rests for
	for _, event := range []string{
		`data: {"type":"error","error":{"type":"api_error","message":"` + cyberPolicyMessage + `"}}`,
		`data: {"type":"error","error":{"type":"devin_error","code":502,"message":"` + cyberPolicyMessage + `"}}`,
	} {
		if kind, status, msg := streamEvent([]byte(event)); kind == eventError || status != refusedStatus || !strings.Contains(msg, "cyber") {
			t.Errorf("error event was not a refusal: %s: kind %d, status %d, message %q", event, kind, status, msg)
		}
	}
	for _, body := range []string{
		// the same shapes without the refusal's words are the request's
		// own 400s: too large a reply, an upstream's own trouble
		`{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: too large"}}`,
		`{"type":"error","error":{"type":"devin_error","code":502,"message":"upstream unavailable"}}`,
		`{"type":"error","error":{"type":"api_error","message":"internal server error"}}`,
		// the policy named without the refusal said is no refusal
		`{"type":"error","error":{"type":"invalid_request_error","message":"asked about the Anthropic cyber verification policy"}}`,
	} {
		if note, ok := policyRefusal([]byte(body)); ok {
			t.Errorf("unrelated error classified as a refusal: %s: %q", body, note)
		}
	}
}

// The same refusals arriving as HTTP statuses: a 400 and a 502 are both
// the filter's word, and neither rests the account — the group's next
// member answers (#7707: the 400 failed the group as other, and the 502
// rested the account as an upstream's failure).
func TestAnthropicPlainTextPolicyRefusalFallsBack(t *testing.T) {
	for _, wire := range []struct {
		name string
		code int
		body string
	}{
		{"400 invalid_request_error", http.StatusBadRequest, `{"type":"error","error":{"type":"invalid_request_error","message":"` + cyberPolicyMessage + `"}}`},
		{"502 devin_error", http.StatusBadGateway, `{"type":"error","error":{"type":"devin_error","code":502,"message":"` + cyberPolicyMessage + `"}}`},
	} {
		t.Run(wire.name, func(t *testing.T) {
			fresh(t)
			a := &scripted{replies: []reply{{wire.code, "application/json", wire.body}}}
			b := &scripted{replies: []reply{{200, "text/event-stream", anthropicAnswer}}}
			scriptedOn(t, "a", provider.Chat, a)
			scriptedOn(t, "b", provider.Anthropic, b)
			refusalGroup(t, "a/m", "b/m")
			s := New()
			code, out := sendRefusalError(t, s, "/v1/chat/completions", chatStreamAsk)
			if code != 200 || !strings.Contains(out, "from b") || a.n != 1 || b.n != 1 {
				t.Fatalf("status %d: %s (a %d, b %d)", code, out, a.n, b.n)
			}
			r := lastRoute(s)
			if len(r.Tries) != 2 || r.Tries[0].Fail != failRefused || r.Tries[0].Rest != nil {
				t.Fatalf("the refusal was not handed on, or rested the account: %+v", r.Tries)
			}
		})
	}
}
