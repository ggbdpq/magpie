package provider

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Switched off, a provider is read no more in the background
// (left_alone.go), but what was last known of it is still shown: the Usage
// page and magpie quota keep its card, dated as of the last read and
// marked as not refreshed (#5863: a subscription switched off to only
// switch its account, whose quota card and quota --json windows vanished
// altogether). What magpie quota wait waits on is a fresh reading still,
// so Allotted keeps the card out.
func TestOffProviderKeepsLastCard(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))
	writeFile(t, filepath.Join(home, ".claude.json"), map[string]any{"oauthAccount": map[string]any{"emailAddress": "a@example.com", "accountUuid": "u-a"}})
	rememberLogins(true)
	if err := SetSwitchOnly("claude", true); err != nil {
		t.Fatal(err)
	}
	if err := SetOff("claude", true); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	keepLast(SubscriptionQuota{Provider: "claude", Name: "Claude Code", User: "a@example.com", Windows: []QuotaWindow{
		{Name: "5 hours", Used: 40, ResetsAt: &later, Span: 5 * time.Hour},
	}}, "")
	forgetQuotas(t)

	c := &subscriptionUsageCache
	c.Lock()
	c.data, c.at, c.asked, c.pending = nil, time.Time{}, false, nil
	c.Unlock()
	t.Cleanup(clearUsageCache)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got := SubscriptionUsage(ctx)
	if len(got) != 1 || got[0].Provider != "claude" {
		t.Fatalf("a switched-off provider's last reading is not shown: %+v", got)
	}
	q := got[0]
	if q.AsOf == nil || !q.ReadDeferred || q.Name != "Claude Code" || q.User != "a@example.com" || len(q.Windows) != 1 || q.Windows[0].Used != 40 {
		t.Fatalf("not the last reading, dated and marked: %+v", q)
	}

	// magpie quota --json tells the same card
	b, err := json.Marshal(QuotaReport(context.Background(), time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); !strings.Contains(s, `"provider":"claude"`) || !strings.Contains(s, `"asOf":"`) || !strings.Contains(s, `"readDeferred":true`) {
		t.Fatalf("quota report's json lost the off provider's card: %s", s)
	}

	// what magpie quota wait waits on is a fresh reading: the card stays
	// out of Allotted
	for _, r := range Allotted(context.Background()) {
		if r.Provider == "claude" {
			t.Fatalf("quota wait would wait on the switched-off provider's last card: %+v", r)
		}
	}
}

// An account removed from magpie (Hidden) has no card at all, even one
// read before; a provider switched off that was never read has nothing to
// show.
func TestOffProviderCardBoundaries(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))
	writeFile(t, filepath.Join(home, ".claude.json"), map[string]any{"oauthAccount": map[string]any{"emailAddress": "a@example.com", "accountUuid": "u-a"}})
	rememberLogins(true)
	if err := Delete("claude"); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	keepLast(SubscriptionQuota{Provider: "claude", Name: "Claude Code", User: "a@example.com", Windows: []QuotaWindow{
		{Name: "5 hours", Used: 40, ResetsAt: &later, Span: 5 * time.Hour},
	}}, "")
	forgetQuotas(t)

	c := &subscriptionUsageCache
	c.Lock()
	c.data, c.at, c.asked, c.pending = nil, time.Time{}, false, nil
	c.Unlock()
	t.Cleanup(clearUsageCache)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, q := range SubscriptionUsage(ctx) {
		if q.Provider == "claude" {
			t.Fatalf("a removed account's card is shown: %+v", q)
		}
	}

	// Codex switched off without ever having been read: nothing to show
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"tokens": map[string]any{"access_token": "x", "account_id": "acct-1"},
	})
	if err := SetOff("codex", true); err != nil {
		t.Fatal(err)
	}
	for _, q := range SubscriptionUsage(ctx) {
		if q.Provider == "codex" {
			t.Fatalf("a card out of nothing for a provider never read: %+v", q)
		}
	}
}

// forgetQuotas drops what keepLast and the tests before it kept, as after
// a restart: the readings are on disk, and the next read loads them again.
// The test ends on the same footing.
func forgetQuotas(t *testing.T) {
	t.Helper()
	resetQuotas()
	t.Cleanup(resetQuotas)
}

func resetQuotas() {
	lastQuotas.Lock()
	lastQuotas.m, lastQuotas.loaded = nil, false
	lastQuotas.Unlock()
}

func clearUsageCache() {
	c := &subscriptionUsageCache
	for {
		c.Lock()
		p := c.pending
		if p == nil {
			c.data, c.at = nil, time.Time{}
			c.Unlock()
			return
		}
		c.Unlock()
		<-p
	}
}
