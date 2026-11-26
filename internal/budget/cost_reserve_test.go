package budget

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// A cost-only limit without CacheReads still prices its in-flight reservation
// against all tokens (input + output + cacheWrite + cacheRead), matching the
// full-context body/4 estimate instead of dividing total cost by uncached
// tokens only (#7803).
func TestCostReservationIncludesCacheReads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	Forget()
	t.Cleanup(Forget)
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	// Input $10/M, Output $30/M, CacheRead $0.50/M, CacheWrite $0/M.
	in, out, cr, cw := 10.0, 30.0, 0.50, 0.0
	if err := settings.Save(settings.Settings{ModelPrices: map[string]settings.ModelPrice{
		"relay/gpt-test": {Input: &in, Output: &out, CacheRead: &cr, CacheWrite: &cw},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "https://relay.example/v1"}); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	// Cost-only weekly limit of $10, CacheReads false (default).
	who := access.Identity{
		KeyID:   "k-cache-cost",
		KeyName: "CachedKey",
		Limit:   &access.Limit{Period: "week", Cost: 10.0, CacheReads: false},
	}
	// Prior call: 2,000 uncached input ($0.02) + 1,000 output ($0.03) + 197,000 cache_read ($0.0985)
	// Total cost = $0.1485 over 200,000 total tokens ($0.7425/M), vs 3,000 uncached ($49.50/M).
	Append(usage.Record{
		Time: now, Provider: "relay", Model: "gpt-test",
		Input: 2000, Output: 1000, CacheRead: 197000,
		Status: 200, CallerKeyID: who.KeyID,
	})

	// Request with 800,000 byte body (~200,000 tokens + 1,000 mean output = 201,000 est tokens).
	// True all-token rate reservation: 201,000 * 0.1485 / 200,000 ≈ $0.149.
	// Buggy uncached-only reservation: 201,000 * 0.1485 / 3,000 ≈ $9.95 -> blocks 2nd parallel call!
	rel1, ref1 := Reserve(who, 800_000, "relay/gpt-test", now)
	if ref1 != nil {
		t.Fatalf("first call refused unexpectedly: %v", ref1)
	}
	defer rel1()

	rel2, ref2 := Reserve(who, 800_000, "relay/gpt-test", now)
	if ref2 != nil {
		t.Fatalf("second parallel call refused due to inflated reservation: %v (reservedCost=%.4f)", ref2, ref2.ReservedCost)
	}
	defer rel2()

	// Verify the recorded reservation cost is around one call (~$0.15 each, ~$0.30 for two), not ~$9.95,
	// while Status.Tokens still excludes cache reads because CacheReads is false.
	st := Of(access.Key{ID: who.KeyID, Name: who.KeyName, Limit: who.Limit}, now)
	if st == nil {
		t.Fatal("expected status")
	}
	if st.ReservedCost > 1.0 {
		t.Fatalf("ReservedCost=%.4f for two ~$0.15 calls; expected < $1.00", st.ReservedCost)
	}
	if st.Tokens != 3000 {
		t.Fatalf("Status.Tokens=%d, want 3000 (CacheReads=false must keep cache reads out of token accounting)", st.Tokens)
	}
}
