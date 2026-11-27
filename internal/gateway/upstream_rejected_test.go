package gateway

import (
	"strings"
	"testing"
)

// upstreamRejectedRefusal is how a relay says the provider behind it
// turned the request away, as a code: invalid_request_error with code
// upstream_request_rejected (a magpie user's #7942: group/
// moeai-codeseek-v1, "有概率这个特定的第三方模型返回 400 错误，然后会
// 话直接中断，不会重试或者切换上游了"). It is the same refusal
// upstreamFailedWords already takes as the relay's own (#4632, #866,
// #1511) — "Upstream request failed", said spaced there — only this
// relay words it as one code, with underscores, and rejected for failed.
const upstreamRejectedRefusal = `{"error":{"message":"Bad Request","type":"invalid_request_error","code":"upstream_request_rejected"}}`

// A relay that says its upstream rejected the request had the group end
// at it: the code's underscores left it unmatched by every wording
// retryable reads, so the next member was never asked and the turn was
// lost. It is asked now, as for "Upstream request failed" (#4632), and
// nothing rests for the relay.
func TestUpstreamRejectedFailsOver(t *testing.T) {
	plan := &fake{t: t, ctype: "application/json", code: 400, reply: upstreamRejectedRefusal}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	twoProviders(t, plan, spare)
	code, body := post(t, "/v1/chat/completions", chatReq)
	if code != 200 || !strings.Contains(body, "from-spare") {
		t.Fatalf("%d %s", code, body)
	}
	if plan.calls != 1 {
		t.Fatalf("plan asked %d times, want 1", plan.calls)
	}
}

// The code is read wherever the spaced wording already is: the group's
// retryable and shapeRefused, and the compaction's in-stream refusal
// (roleRefused), which reads the error's message alone.
func TestUpstreamRejectedWords(t *testing.T) {
	for _, said := range []string{
		"upstream_request_rejected",
		"upstream_request_failed",
		"Upstream request failed",
		"Upstream Request Rejected",
	} {
		if !upstreamFailedWords.MatchString(said) {
			t.Errorf("upstreamFailedWords(%q) = false", said)
		}
	}
	if upstreamFailedWords.MatchString("upstream_request_expired") {
		t.Error("upstreamFailedWords matched a word of another kind")
	}
	if !retryable(400, []byte(upstreamRejectedRefusal)) {
		t.Fatal("retryable(400, upstream_request_rejected) = false")
	}
	if !shapeRefused(400, []byte(upstreamRejectedRefusal)) {
		t.Fatal("shapeRefused(400, upstream_request_rejected) = false")
	}
}
