package provider

import (
	"testing"
	"time"
)

func TestClaudeLimitSaidMatchesOutOfExtraUsage(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	msg := "API Error: 400 You're out of extra usage. Add more at claude.ai/settings/usage and keep going."
	if !claudeLimitSaid.MatchString(msg) {
		t.Fatalf("claudeLimitSaid(%q) = false, want true", msg)
	}
	if fam := claudeLimitFamily("", msg); fam != "" {
		t.Fatalf("claudeLimitFamily = %q, want empty (whole account)", fam)
	}

	// Negative guard: third-party refusal must not mark the account out.
	thirdParty := "API Error: 400 Third-party apps now draw from your extra usage, not your plan limits."
	if claudeLimitSaid.MatchString(thirdParty) {
		t.Fatalf("claudeLimitSaid(%q) = true, want false", thirdParty)
	}

	now := time.Now()
	noteClaudeSaid("primary@example.com", msg, now)
	if !claudeSaidOut("primary@example.com", now.Add(time.Minute)) {
		t.Fatal("noteClaudeSaid did not keep account out on out-of-extra-usage refusal")
	}
	if !claudeOutUnread("primary@example.com", nil, true, SubscriptionQuota{Error: msg}, now) {
		t.Fatal("claudeOutUnread = false for out-of-extra-usage error, want true")
	}
}
