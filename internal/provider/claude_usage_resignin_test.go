package provider

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// A re-login for the account Claude Code is signed in to (a plan move on
// it: a Max 5x upgrade, delete-cloud) swaps the credential under a kept
// reading. What the sign-in before told — every window full of a plan the
// new one never shared — served on while the account's real allowance was
// 0% on every window, until a hand refresh. The kept reading gives way
// with the sign-in it was told by (#5921).
func TestClaudeUsageOfASignInBeforeGivesWay(t *testing.T) {
	home := claudeHome(t)
	cred := claudeSignIn(t, home, time.Now().Add(time.Hour))
	writeFile(t, filepath.Join(home, ".claude.json"), map[string]any{"oauthAccount": map[string]any{"emailAddress": "a@example.com", "accountUuid": "u-a"}})

	var out atomic.Value
	out.Store("Current session: 5% used · resets " + sessionReset() + "\nCurrent week (all models): 20% used · resets " + soon(3) + " at 2pm (UTC)\n")
	runs := fakeClaudeUsage(t, &out, nil)
	oldUsed := claudeUsedSince
	claudeUsedSince = func(time.Time) bool { return false } // a headless gateway: no session of its own
	t.Cleanup(func() { claudeUsedSince = oldUsed })

	// a reading kept from the hour before, told by the sign-in then in
	// place: every window of it full
	T0 := time.Now().Add(-time.Hour)
	claudeUsage.Lock()
	claudeUsage.m = map[string]claudeUsageEntry{"a@example.com": {at: T0, ws: []QuotaWindow{
		{Name: "Current session", Used: 100, Span: 5 * time.Hour},
		{Name: "Current week (all models)", Used: 100, Span: 7 * 24 * time.Hour},
	}, heard: T0, tried: T0, whole: true}}
	claudeUsage.Unlock()

	// a re-login in place: fresh tokens, the tier moved, the profile
	// still the account's
	writeFile(t, cred, map[string]any{"claudeAiOauth": map[string]any{
		"accessToken": "sk-ant-oat01-new", "refreshToken": "sk-ant-ort01-new",
		"expiresAt": time.Now().Add(time.Hour).UnixMilli(), "subscriptionType": "max",
		"rateLimitTier": "default_claude_max_5x"}})
	forgetClaudeCredential()

	// the Usage card's unasked look serves no window of the sign-in
	// before, and reads again instead
	ws, err := claudeWindows(context.Background(), "a@example.com", true)
	if err != nil || len(ws) == 0 || ws[0].Used != 5 {
		t.Fatalf("the kept reading of the sign-in before served: %v %+v (%d runs)", err, ws, runs.Load())
	}
	if runs.Load() != 1 {
		t.Fatalf("%d runs, want the one the re-login's look made", runs.Load())
	}
}

// A sign-in magpie made itself drops what the sign-in before told: the
// card named the new plan while its windows still said the old one (#5921).
func TestSignInDropsTheKeptAllowance(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))
	writeFile(t, filepath.Join(home, ".claude.json"), map[string]any{"oauthAccount": map[string]any{"emailAddress": "a@example.com", "accountUuid": "u-a"}})

	var out atomic.Value
	out.Store("Current session: 5% used · resets " + sessionReset() + "\n")
	runs := fakeClaudeUsage(t, &out, nil)
	oldUsed := claudeUsedSince
	claudeUsedSince = func(time.Time) bool { return false }
	t.Cleanup(func() { claudeUsedSince = oldUsed })

	T0 := time.Now().Add(-time.Hour)
	claudeUsage.Lock()
	claudeUsage.m = map[string]claudeUsageEntry{"a@example.com": {at: T0, ws: []QuotaWindow{
		{Name: "Current week (all models)", Used: 100},
	}, heard: T0, tried: T0, whole: true}}
	claudeUsage.Unlock()

	l, err := claudeLogin(claudeCredentials{OAuth: claudeAuth{
		AccessToken: "tok-new", RefreshToken: "r-new",
		ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), SubscriptionType: "max",
		RateLimitTier: "default_claude_max_5x",
	}}, map[string]any{"emailAddress": "a@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := addLogin(l); err != nil {
		t.Fatal(err)
	}
	if at := claudeReadAt("a@example.com"); at != nil {
		t.Fatalf("the kept reading of the sign-in before stands after the new one: %s", at.Format(time.RFC3339))
	}
	// the next look reads the new sign-in
	ws, err := claudeWindows(context.Background(), "a@example.com", true)
	if err != nil || len(ws) == 0 || ws[0].Used != 5 {
		t.Fatalf("not read again under the new sign-in: %v %+v (%d runs)", err, ws, runs.Load())
	}
}
