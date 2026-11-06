package provider

import (
	"context"
	"sync"
	"testing"
	"time"
)

// clearRoutingCaches has routing read again: what another test left of the
// same accounts' readings, in memory or on disk, is not this test's.
func clearRoutingCaches(t *testing.T) {
	t.Helper()
	usedCache.Lock()
	usedCache.m, usedCache.at, usedCache.loading = map[string]map[string]Allowance{}, map[string]time.Time{}, map[string]chan struct{}{}
	usedCache.stale, usedCache.renewed, usedCache.seen, usedCache.read = nil, nil, nil, nil
	usedCache.Unlock()
	loginUsageCache.Lock()
	loginUsageCache.m, loginUsageCache.pending = nil, nil
	loginUsageCache.Unlock()
	lastQuotas.Lock()
	lastQuotas.m, lastQuotas.loaded = nil, false
	lastQuotas.Unlock()
	t.Cleanup(func() {
		usedCache.Lock()
		usedCache.m, usedCache.at, usedCache.loading = map[string]map[string]Allowance{}, map[string]time.Time{}, map[string]chan struct{}{}
		usedCache.stale, usedCache.renewed, usedCache.seen, usedCache.read = nil, nil, nil, nil
		usedCache.Unlock()
		loginUsageCache.Lock()
		loginUsageCache.m, loginUsageCache.pending = nil, nil
		loginUsageCache.Unlock()
	})
}

// #5079: a careful provider's re-read (ReadUsageNow, cap_careful.go) of a
// saved Claude account has to be a read of it. Its /usage runs for no ask
// of routing's — only its card's refresh, the refresh of every allowance,
// the switch off a spent one and the keep-alive reached it — so the re-read
// came back as what was kept, and a capped account was held for good: #5085
// held it as unread, its reading's real age held it after. The ask now
// reaches it, with the 30-second floor and a failed read's wait as the cap
// on its runs, so the reading that comes back is one worth routing by.
func TestReadUsageNowReadsASavedClaudeAccount(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))
	writeFile(t, loginsPath(), []map[string]any{
		{"agent": "claude", "user": "spare@example.com", "plan": "max", "on": true, "seen": time.Now(),
			"auth": map[string]any{"claudeAiOauth": map[string]any{
				"accessToken": "tok-spare", "refreshToken": "r-spare",
				"expiresAt": time.Now().Add(24 * time.Hour).UnixMilli(), "subscriptionType": "max"}}},
	})
	forgetAccountCaches()
	rememberLogins(true)
	t.Cleanup(func() { forgetAccountCaches(); ForgetAccounts() })
	var mu sync.Mutex
	var savedRuns int
	UsageClaudeVia(func(_ context.Context, dir string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if dir != "" { // a saved account's own directory: the account is being read
			savedRuns++
		}
		return "Current session: 30% used", nil
	})
	t.Cleanup(func() { UsageClaudeVia(nil) })
	claudeUsage.Lock()
	claudeUsage.m = nil
	claudeUsage.Unlock()
	readAsked.Lock()
	readAsked.at = nil
	readAsked.Unlock()
	clearRoutingCaches(t)
	t.Cleanup(func() {
		claudeUsage.Lock()
		claudeUsage.m = nil
		claudeUsage.Unlock()
		readAsked.Lock()
		readAsked.at = nil
		readAsked.Unlock()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !ReadUsageNow(ctx, "claude", "spare@example.com") {
		t.Fatal("no reading came back")
	}
	mu.Lock()
	runs := savedRuns
	mu.Unlock()
	if runs == 0 {
		t.Fatal("the re-read ask never read the saved account: /usage was not run in its directory")
	}
	at, failed, ok := UsageRead("claude", "spare@example.com")
	if !ok || failed {
		t.Fatalf("a read that went through: failed %v ok %v", failed, ok)
	}
	if time.Since(at) > time.Minute {
		t.Fatalf("the reading stands dated as it was read: at %v", at)
	}
	if _, known := Allowances("claude")["spare@example.com"]; !known {
		t.Fatal("the saved account is not known to routing after its read")
	}
}
