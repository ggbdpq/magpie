package provider

import (
	"testing"
)

// TestUnlimitedQuotaBalance: a new-api relay's key-usage reply, asked for
// what is left on the key, says "unlimited_quota": true on a key with no
// quota of its own — its total_available is what the key has spent, and it
// goes negative once use passes the token's grant, so the balance magpie
// shows went negative where the account held money (#5436, AnxForever:
// $-4.33 shown, $106.44 held). The account's own total_available, under
// user_quota in the same reply, in the same units, is the balance then.
// A key with a quota of its own keeps reading the key's field, and a
// Balance field the user set by hand is never second-guessed.
func TestUnlimitedQuotaBalance(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	payload := []byte(`{"code":true,"data":{"total_available":-2192414,"total_granted":0,"total_used":2192414,"unlimited_quota":true,"user_quota":{"total_available":53222012,"total_used":6778929}}}`)
	p := Provider{ID: "arm", Name: "Arm", Chat: "https://ai.armclouding.com/v1", Key: "sk-a", BalanceURL: "https://ai.armclouding.com/api/usage/token"}
	src, ok := balanceSourceOf(p)
	if !ok {
		t.Fatal("no balance source")
	}
	got, err := src.read(payload)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != "$106.44" {
		t.Fatalf("balance %q, want $106.44 (the account's own, not the unlimited key's spent)", got)
	}

	limited := []byte(`{"code":true,"data":{"total_available":2500000,"total_granted":5000000,"total_used":2500000,"unlimited_quota":false}}`)
	got, err = src.read(limited)
	if err != nil {
		t.Fatalf("read limited: %v", err)
	}
	if got != "$5.00" {
		t.Fatalf("limited key's balance %q, want $5.00", got)
	}

	byHand := Provider{ID: "arm", Name: "Arm", Chat: "https://ai.armclouding.com/v1", Key: "sk-a", BalanceURL: "https://ai.armclouding.com/api/usage/token", BalancePath: "$data.total_available / 500000"}
	src, ok = balanceSourceOf(byHand)
	if !ok {
		t.Fatal("no balance source by hand")
	}
	got, err = src.read(payload)
	if err != nil {
		t.Fatalf("read by hand: %v", err)
	}
	if got != "$-4.38" {
		t.Fatalf("a hand-set field was second-guessed: %q (want the key's own negative)", got)
	}

	// shrekcg's three relays (#5436): two carry no user_quota at all —
	// the token-scoped figure is what the key has spent, and it reads as
	// a negative balance where there is no balance to show. A limited
	// key spent past its grant reads negative the same way.
	if src, ok = balanceSourceOf(p); !ok {
		t.Fatal("no balance source")
	}
	noUserQuota := []byte(`{"code":true,"message":"","data":{"name":"x","object":"token","expires_at":0,"model_limits":{},"model_limits_enabled":false,"total_available":-4469172,"total_granted":0,"total_used":4469172,"unlimited_quota":true}}`)
	got, err = src.read(noUserQuota)
	if err != nil {
		t.Fatalf("read relay A: %v", err)
	}
	if got != "" {
		t.Fatalf("relay A (unlimited, no user_quota) balance %q, want no amount: a grant spent past its end is not a balance", got)
	}

	overspent := []byte(`{"code":true,"data":{"total_available":-349725,"total_used":349725,"unlimited_quota":false}}`)
	got, err = src.read(overspent)
	if err != nil {
		t.Fatalf("read overspent: %v", err)
	}
	if got != "" {
		t.Fatalf("overspent limited key balance %q, want no amount", got)
	}

	// relay B carries user_quota: the account's own figure wins as before
	relayB := []byte(`{"code":true,"data":{"total_available":-349725,"total_used":349725,"unlimited_quota":true,"user_quota":{"total_available":49650275,"total_used":349725}}}`)
	got, err = src.read(relayB)
	if err != nil {
		t.Fatalf("read relay B: %v", err)
	}
	if got != "$99.30" {
		t.Fatalf("relay B balance %q, want $99.30 (the account's own)", got)
	}
}
