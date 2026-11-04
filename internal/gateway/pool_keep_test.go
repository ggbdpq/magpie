package gateway

// wssz527 (#4030): a pool whose accounts answer "overloaded" 429s for
// seconds pays its conversations' whole prompts again whenever a
// transient rest moves them to the pool's other account — restLast moves
// a resting one behind the rest whatever the routing ordered, so no
// strategy keeps it, and the cache its account had built is wasted. A
// pool set KeepThroughRest says a rest out of a rate limit or a failure
// that says nothing of why is no reason to move its accounts: they keep
// the place the routing gave them, and the trace still says they rest. A
// rest out of quota or credit still moves them, and so does every rest
// of a pool without the setting.
import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// restAt has key resting for a little while, for why.
func restAt(t *testing.T, key, why string) {
	t.Helper()
	restingUntil.Lock()
	restingUntil.m[key] = time.Now().Add(30 * time.Second)
	restingUntil.note[key] = Rest{Why: why}
	restingUntil.Unlock()
	t.Cleanup(func() {
		restingUntil.Lock()
		delete(restingUntil.m, key)
		delete(restingUntil.note, key)
		restingUntil.Unlock()
	})
}

// A pool keeping through a rest: its account resting out of a rate limit,
// or after a failure that says nothing of why, is asked where the routing
// put it, and the trace says it rests all the same.
func TestPoolKeepsThroughARateRest(t *testing.T) {
	for _, why := range []string{failRate, failOther} {
		p := provider.Provider{ID: "kimi", KeepThroughRest: true}
		resting := candidate{p: p, model: "m", rest: "kimi@one"}
		ready := candidate{p: p, model: "m", rest: "kimi@two"}
		restAt(t, "kimi@one", why)
		out, pl := restLast([]candidate{resting, ready}, planned{order: make([]Weighed, 2)})
		if out[0].rest != "kimi@one" {
			t.Fatalf("%s: the resting one moved behind: first %s", why, out[0].rest)
		}
		if pl.order[0].Rest == nil || pl.order[0].Rest.Why != why {
			t.Fatalf("%s: the trace says nothing of its rest: %+v", why, pl.order[0].Rest)
		}
		// ready ahead of it, it follows as it did: nothing promotes it
		out, _ = restLast([]candidate{ready, resting}, planned{order: make([]Weighed, 2)})
		if out[0].rest != "kimi@two" {
			t.Fatalf("%s: the resting one was put ahead: first %s", why, out[0].rest)
		}
	}
}

// A rest out of quota or credit still moves a keeping pool's account
// behind the rest: those are not over in seconds. And without the
// setting, a rate rest moves it as it did.
func TestPoolStillMovesOnQuotaAndWithoutTheSetting(t *testing.T) {
	for _, why := range []string{failQuota, failCredit} {
		p := provider.Provider{ID: "kimi", KeepThroughRest: true}
		resting := candidate{p: p, model: "m", rest: "kimi@one"}
		ready := candidate{p: p, model: "m", rest: "kimi@two"}
		restAt(t, "kimi@one", why)
		out, _ := restLast([]candidate{resting, ready}, planned{order: make([]Weighed, 2)})
		if out[0].rest != "kimi@two" {
			t.Fatalf("%s: the resting one kept its place: first %s", why, out[0].rest)
		}
	}
	p := provider.Provider{ID: "kimi"}
	resting := candidate{p: p, model: "m", rest: "kimi@one"}
	ready := candidate{p: p, model: "m", rest: "kimi@two"}
	restAt(t, "kimi@one", failRate)
	out, _ := restLast([]candidate{resting, ready}, planned{order: make([]Weighed, 2)})
	if out[0].rest != "kimi@two" {
		t.Fatalf("no setting: the resting one kept its place: first %s", out[0].rest)
	}
}
