package provider

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/netproxy"
)

// plugins#349: a careful provider asks when each account's usage was
// last read well and whether the last try failed (UsageRead), and has it
// read again before a request (ReadUsageNow). A failed try keeps the last
// good reading's time, so its age still counts.
func TestUsageReadAndReadAgain(t *testing.T) {
	signIn(t)
	resets := time.Now().Add(3 * time.Hour)
	var meFails atomic.Bool
	LoginUsageVia(func(context.Context, string) map[string]SubscriptionQuota {
		me := SubscriptionQuota{Windows: []QuotaWindow{{Span: 5 * time.Hour, Used: 40, ResetsAt: &resets}}}
		if meFails.Load() {
			me = SubscriptionQuota{Error: "usage endpoint said 503"}
		}
		return map[string]SubscriptionQuota{
			"me@example.com":    me,
			"other@example.com": {Error: "usage endpoint said 401"},
		}
	})
	clear := func() {
		usedCache.Lock()
		usedCache.m, usedCache.at, usedCache.loading = map[string]map[string]Allowance{}, map[string]time.Time{}, map[string]chan struct{}{}
		usedCache.stale, usedCache.renewed, usedCache.seen, usedCache.read = nil, nil, nil, nil
		usedCache.Unlock()
	}
	clear()
	t.Cleanup(func() {
		usedCache.Lock()
		done := usedCache.loading["codex"]
		usedCache.Unlock()
		if done != nil {
			<-done
		}
		LoginUsageVia(nil)
		clear()
	})
	if _, _, ok := UsageRead("codex", "me@example.com"); ok {
		t.Fatal("known before any read")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	before := time.Now()
	if !ReadUsageNow(ctx, "codex", "me@example.com") {
		t.Fatal("no reading came back")
	}
	at, failed, ok := UsageRead("codex", "ME@example.com")
	if !ok || failed || at.Before(before) {
		t.Fatalf("me: at %v (asked %v) failed %v ok %v", at, before, failed, ok)
	}
	if _, failed, ok := UsageRead("codex", "other@example.com"); !ok || !failed {
		t.Fatalf("other: failed %v ok %v", failed, ok)
	}

	// read again: a newer good reading
	good := at
	if !ReadUsageNow(ctx, "codex", "me@example.com") {
		t.Fatal("no second reading")
	}
	if at, failed, _ := UsageRead("codex", "me@example.com"); failed || !at.After(good) {
		t.Fatalf("read again: at %v, was %v, failed %v", at, good, failed)
	}
	good, _, _ = UsageRead("codex", "me@example.com")

	// it fails: failed, the last good reading's time kept
	meFails.Store(true)
	if !ReadUsageNow(ctx, "codex", "me@example.com") {
		t.Fatal("no third reading")
	}
	if at, failed, _ := UsageRead("codex", "me@example.com"); !failed || !at.Equal(good) {
		t.Fatalf("failed read: at %v, good %v, failed %v", at, good, failed)
	}
}

// A reading kept from before that no one asked for again (ReadDeferred,
// #5085: a saved Claude account's, read only when its card is refreshed,
// the switch off a spent one needs it, or the keep-alive runs) is what
// the account last said, and is not a failed read: it stands as its
// reading, dated as it was really read (its AsOf), the way a kept
// reading in memory is dated by its readAt, so a careful provider holds
// it for its age alone, not as one whose usage couldn't be told, and
// never counts a reading of any age as just read (#5085 review). A read
// that failed and was answered from what was kept is a failed one, as
// before.
func TestUsageReadDeferredIsNotAFailedRead(t *testing.T) {
	signIn(t)
	resets := time.Now().Add(3 * time.Hour)
	kept := time.Now().Add(-2 * time.Hour)
	var fail atomic.Bool
	LoginUsageVia(func(context.Context, string) map[string]SubscriptionQuota {
		q := SubscriptionQuota{Windows: []QuotaWindow{{Span: 5 * time.Hour, Used: 30, ResetsAt: &resets}}, AsOf: &kept, ReadDeferred: true}
		if fail.Load() {
			q.ReadDeferred = false // the read failed; what is shown was kept
		}
		return map[string]SubscriptionQuota{"me@example.com": q}
	})
	clear := func() {
		usedCache.Lock()
		usedCache.m, usedCache.at, usedCache.loading = map[string]map[string]Allowance{}, map[string]time.Time{}, map[string]chan struct{}{}
		usedCache.stale, usedCache.renewed, usedCache.seen, usedCache.read = nil, nil, nil, nil
		usedCache.Unlock()
	}
	clear()
	t.Cleanup(func() {
		usedCache.Lock()
		done := usedCache.loading["codex"]
		usedCache.Unlock()
		if done != nil {
			<-done
		}
		LoginUsageVia(nil)
		clear()
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	before := time.Now()
	if !ReadUsageNow(ctx, "codex", "me@example.com") {
		t.Fatal("no reading came back")
	}
	at, failed, ok := UsageRead("codex", "me@example.com")
	if !ok || failed {
		t.Fatalf("a kept reading no one asked for again: failed %v ok %v", failed, ok)
	}
	if at.After(before) {
		t.Fatalf("a kept reading counts as just read: at %v, asked %v, read %v", at, before, kept)
	}

	// a read that failed, answered from what was kept: failed, as before
	fail.Store(true)
	if !ReadUsageNow(ctx, "codex", "me@example.com") {
		t.Fatal("no second reading")
	}
	if _, failed, _ = UsageRead("codex", "me@example.com"); !failed {
		t.Fatal("a failed read answered from what was kept: not failed")
	}
}

// A reading handed over from disk (nothing read since magpie started,
// a held read is answered by keepLast with the last one, as it stands
// now) carries its real read time — the snapshot's At, handed over as
// AsOf — and stands dated by it, the way a kept reading in memory is
// dated by its readAt: a careful provider counts its age, and holds it
// when it is too old, rather than trusting a reading of any age as one
// just made (#5085 review).
func TestUsageReadKeptOnDiskKeepsItsReadTime(t *testing.T) {
	signIn(t)
	reset := func() {
		LoginUsageVia(nil)
		loginUsageCache.Lock()
		loginUsageCache.m, loginUsageCache.pending = nil, nil
		loginUsageCache.Unlock()
		lastQuotas.Lock()
		lastQuotas.m, lastQuotas.loaded = nil, false
		lastQuotas.Unlock()
	}
	reset()
	t.Cleanup(reset)
	rememberLogins(true) // the logins list is shared: this home's is the one to read
	// a restart: nothing read since, what magpie kept on disk is all it
	// has, read two hours ago
	resets := time.Now().Add(3 * time.Hour)
	kept := time.Now().Add(-2 * time.Hour)
	lastQuotas.Lock()
	lastQuotas.m = map[string]lastQuota{
		"codex/me@example.com": {At: kept, Q: SubscriptionQuota{Provider: "codex", Windows: []QuotaWindow{{Span: 5 * time.Hour, Used: 30, ResetsAt: &resets}}}},
	}
	lastQuotas.loaded = true
	lastQuotas.Unlock()
	ctx := netproxy.Hold(context.Background(), errNotAsked) // a read held back: what was kept stands in
	_, _, states := loginUsageAt(ctx, "codex")
	st, ok := states["me@example.com"]
	if !ok {
		t.Fatal("no reading")
	}
	if st.failed {
		t.Fatal("a reading kept on disk, no one asked for again: failed")
	}
	if st.at.After(kept.Add(time.Minute)) {
		t.Fatalf("a reading kept on disk counts as just read: at %v, read %v", st.at, kept)
	}
}

// An account within the given points of a window's cap, or past it, is
// near it; one further off, a window renewed since, or no cap is not.
func TestNearCap(t *testing.T) {
	now := time.Now()
	al := func(used float64, resets time.Time) Allowance {
		return Allowance{{Used: used, Resets: resets, Span: 5 * time.Hour}}
	}
	later, earlier := now.Add(time.Hour), now.Add(-time.Minute)
	caps := WindowCaps{All: 90}
	for _, c := range []struct {
		name string
		a    Allowance
		caps WindowCaps
		want bool
	}{
		{"within five points", al(86, later), caps, true},
		{"past it", al(95, later), caps, true},
		{"further off", al(80, later), caps, false},
		{"renewed since", al(88, earlier), caps, false},
		{"no cap", al(99, later), WindowCaps{}, false},
	} {
		if got := c.a.NearCap("gpt-5.5", c.caps, 5, now); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}

// Stop at the caps and Check usage near the cap are kept with the
// provider, off until set, and a save of the rest keeps them.
func TestCapStopAndCarefulKept(t *testing.T) {
	isolate(t)
	if err := Save(Provider{ID: "plan", Name: "Plan", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("plan"); p.CapStop || p.CapCareful {
		t.Fatalf("on by default: %+v", p)
	}
	if err := SetCapStop("plan", true); err != nil {
		t.Fatal(err)
	}
	if err := SetCapCareful("plan", true); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("plan")
	if !p.CapStop || !p.CapCareful {
		t.Fatalf("not kept: stop %v careful %v", p.CapStop, p.CapCareful)
	}
	p.Name = "Plan 2"
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("plan"); !p.CapStop || !p.CapCareful || p.Name != "Plan 2" {
		t.Fatalf("after a save: %+v", p)
	}
	if err := SetCapStop("plan", false); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("plan"); p.CapStop || !p.CapCareful {
		t.Fatalf("stop off: stop %v careful %v", p.CapStop, p.CapCareful)
	}
}
