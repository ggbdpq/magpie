package gateway

import (
	"net/http"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

const outOfExtraUsage400Body = `{"type":"error","error":{"type":"invalid_request_error","message":"API Error: 400 You're out of extra usage. Add more at claude.ai/settings/usage and keep going."}}`

func TestOutOfExtraUsage400ClassifiedAsQuotaAndRetryable(t *testing.T) {
	for _, body := range []string{
		outOfExtraUsage400Body,
		`Claude Code: API Error: 400 You're out of extra usage. Add more at claude.ai/settings/usage and keep going.`,
		`{"type":"error","error":{"message":"You\u2019re out of extra usage. Add more at claude.ai/settings/usage and keep going."}}`,
	} {
		if f := failure(http.StatusBadRequest, []byte(body)); f != failQuota {
			t.Errorf("failure(400, %s) = %q, want %q", body, f, failQuota)
		}
		if !retryable(http.StatusBadRequest, []byte(body)) {
			t.Errorf("retryable(400, %s) = false, want true", body)
		}
		if shapeRefused(http.StatusBadRequest, []byte(body)) {
			t.Errorf("shapeRefused(400, %s) = true, want false", body)
		}
		if modelRefused(http.StatusBadRequest, []byte(body)) {
			t.Errorf("modelRefused(400, %s) = true, want false", body)
		}
	}

	if !quotaSaid("API Error: 400 You're out of extra usage. Add more at claude.ai/settings/usage and keep going.") {
		t.Errorf("quotaSaid(out of extra usage) = false, want true")
	}
	if code, _ := bridgeFailed("Claude Code", "You're out of extra usage. Add more at claude.ai/settings/usage and keep going.", 0); code != http.StatusTooManyRequests {
		t.Errorf("bridgeFailed(..., status=0) = %d, want 429", code)
	}
	if code, _ := bridgeFailed("Claude Code", "API Error: 400 You're out of extra usage. Add more at claude.ai/settings/usage and keep going.", http.StatusBadRequest); code != http.StatusBadRequest {
		t.Errorf("bridgeFailed(..., status=400) = %d, want 400", code)
	}

	// Negative guard: Anthropic's third-party prompt refusal mentions "extra usage"
	// but is about the prompt shape/mode, not an exhausted account.
	thirdParty := `{"type":"error","error":{"type":"invalid_request_error","message":"Third-party apps now draw from your extra usage, not your plan limits. Add more at claude.ai/settings/usage and keep going."}}`
	if f := failure(http.StatusBadRequest, []byte(thirdParty)); f != failOther {
		t.Errorf("third-party refusal failure = %q, want %q", f, failOther)
	}
	if retryable(http.StatusBadRequest, []byte(thirdParty)) {
		t.Errorf("third-party refusal retryable = true, want false")
	}
	if quotaSaid(thirdParty) {
		t.Errorf("third-party refusal quotaSaid = true, want false")
	}

	// Related case noted in #7212: API key credit balance too low on 400.
	creditLow := `{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API."}}`
	if f := failure(http.StatusBadRequest, []byte(creditLow)); f != failCredit {
		t.Errorf("credit balance too low failure = %q, want %q", f, failCredit)
	}
	if !retryable(http.StatusBadRequest, []byte(creditLow)) {
		t.Errorf("credit balance too low retryable = false, want true")
	}
}

func TestOutOfExtraUsage400FailsOverGroupAndRestsExhaustedMember(t *testing.T) {
	fresh(t)
	primary := &scripted{replies: []reply{{http.StatusBadRequest, "", outOfExtraUsage400Body}}}
	backup := &scripted{replies: []reply{{http.StatusOK, "", chatOK}, {http.StatusOK, "", chatOK}}}
	scriptedOn(t, "primary", provider.Chat, primary)
	scriptedOn(t, "backup", provider.Chat, backup)
	if err := provider.SaveGroup(provider.Group{
		Name: "MainOpus", Members: []string{"primary/m", "backup/m"}, Routing: provider.Ordered,
	}); err != nil {
		t.Fatal(err)
	}
	s := New()

	code, body := postAs(t, s, "", `{"model":"group/mainopus","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "hello") || primary.n != 1 || backup.n != 1 {
		t.Fatalf("first: %d %s (primary %d, backup %d)", code, body, primary.n, backup.n)
	}
	rt := s.trace.routes[len(s.trace.routes)-1]
	if len(rt.Tries) != 2 || rt.Tries[0].Status != 400 || rt.Tries[0].Fail != failQuota || rt.Tries[0].Rest == nil || rt.Tries[0].Rest.Why != failQuota {
		t.Fatalf("tries = %+v, want primary 400 rested as failQuota", rt.Tries)
	}

	// Next request skips the rested exhausted member and goes straight to backup.
	code, body = postAs(t, s, "", `{"model":"group/mainopus","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "hello") || primary.n != 1 || backup.n != 2 {
		t.Fatalf("second: %d %s (primary %d, backup %d)", code, body, primary.n, backup.n)
	}
}

func TestOutOfExtraUsage400LastCandidateRestsAndNotesClaudeOut(t *testing.T) {
	fresh(t)
	var staled []string
	oldStale := staleAllowance
	staleAllowance = func(agent, user string) { staled = append(staled, agent+":"+user) }
	t.Cleanup(func() { staleAllowance = oldStale })

	// 1. Direct Claude subscription account rest records claude-limits.json and refreshes allowance.
	s := New()
	acct := candidate{
		p:     provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "primary@example.com"}},
		model: "claude-opus-5-5",
		rest:  "claude:primary@example.com",
	}
	r := s.restAfter(acct, http.StatusBadRequest, http.Header{}, []byte(outOfExtraUsage400Body))
	if r.Why != failQuota || r.Key != acct.restKey() {
		t.Fatalf("claude rest = %+v, want Why=%q whole-account Key=%q", r, failQuota, acct.restKey())
	}
	if len(staled) != 1 || staled[0] != "claude:primary@example.com" {
		t.Fatalf("staleAllowance calls = %v, want [claude:primary@example.com]", staled)
	}
	until, said, ok := keptOut(t, "primary@example.com")
	if !ok || !until.Equal(r.Until) || !strings.Contains(said, "out of extra usage") {
		t.Fatalf("claude-limits.json keptOut = (%v, %q, ok=%v), want until %v", until, said, ok, r.Until)
	}

	// 2. Single-candidate (last == true) HTTP request returning 400 "out of extra usage" must still rest.
	only := &scripted{replies: []reply{{http.StatusBadRequest, "", outOfExtraUsage400Body}}}
	scriptedOn(t, "solo", provider.Chat, only)
	code, _ := postAs(t, s, "", `{"model":"solo/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("solo status = %d, want 400", code)
	}
	soloP, err := provider.Find("solo")
	if err != nil {
		t.Fatal(err)
	}
	if sr, ok := RestOf(soloP.RestKey(soloP.KeyList()[0])); !ok || sr.Why != failQuota || sr.Status != http.StatusBadRequest {
		t.Fatalf("solo rest = %+v (ok=%v), want Why=%q Status=400", sr, ok, failQuota)
	}
}
