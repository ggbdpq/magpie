package gateway

// wssz527 (#4030): a pool whose accounts answer "overloaded" 429s for
// seconds pays its conversations' whole prompts again whenever a
// transient rest moves them to the pool's other account. A pool set
// KeepThroughRest says a rest out of a rate limit, or a failure that
// says nothing of why, is no reason to move what its accounts answer.
// restLast holds the request back while an account rests either way —
// a ready one answers, and the resting one is kept from being asked to
// death — and marks a keeping candidate Keeps; affine
// (pool_keep_affine_test.go) is what holds the conversation's seat
// through the rest. A rest out of quota or credit still moves it, and
// so does every rest of a pool without the setting.
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

// A keeping pool's account resting out of a rate limit, or after a
// failure that says nothing of why, is held back with its rest recorded
// and Keeps set: the ready one answers this request, and affine holds
// the conversation's seat until the rest is over.
func TestPoolKeepsThroughARateRest(t *testing.T) {
	for _, why := range []string{failRate, failOther} {
		p := provider.Provider{ID: "kimi", KeepThroughRest: true}
		resting := candidate{p: p, model: "m", rest: "kimi@one"}
		ready := candidate{p: p, model: "m", rest: "kimi@two"}
		restAt(t, "kimi@one", why)
		out, pl := restLast([]candidate{resting, ready}, planned{order: make([]Weighed, 2)})
		if out[0].rest != "kimi@two" {
			t.Fatalf("%s: the resting one was asked first: first %s", why, out[0].rest)
		}
		if pl.order[1].Rest == nil || pl.order[1].Rest.Why != why {
			t.Fatalf("%s: the trace says nothing of its rest: %+v", why, pl.order[1].Rest)
		}
		if !pl.order[1].Keeps {
			t.Fatalf("%s: the trace doesn't say the pool keeps through it", why)
		}
		// a ready one ahead of it stays ahead: nothing promotes it
		out, _ = restLast([]candidate{{p: p, model: "m", rest: "kimi@two"}, {p: p, model: "m", rest: "kimi@one"}}, planned{order: make([]Weighed, 2)})
		if out[0].rest != "kimi@two" {
			t.Fatalf("%s: the resting one was put ahead: first %s", why, out[0].rest)
		}
	}
}

// A rest out of quota or credit holds a keeping pool's account back
// without the Keeps mark: those are not over in seconds. And without
// the setting, a rate rest holds it back unmarked as it did.
func TestPoolStillMovesOnQuotaAndWithoutTheSetting(t *testing.T) {
	for _, why := range []string{failQuota, failCredit} {
		p := provider.Provider{ID: "kimi", KeepThroughRest: true}
		resting := candidate{p: p, model: "m", rest: "kimi@one"}
		ready := candidate{p: p, model: "m", rest: "kimi@two"}
		restAt(t, "kimi@one", why)
		out, pl := restLast([]candidate{resting, ready}, planned{order: make([]Weighed, 2)})
		if out[0].rest != "kimi@two" {
			t.Fatalf("%s: the resting one was asked first: first %s", why, out[0].rest)
		}
		if pl.order[1].Keeps {
			t.Fatalf("%s: the trace says the pool keeps through it", why)
		}
	}
	p := provider.Provider{ID: "kimi"}
	resting := candidate{p: p, model: "m", rest: "kimi@one"}
	ready := candidate{p: p, model: "m", rest: "kimi@two"}
	restAt(t, "kimi@one", failRate)
	out, pl := restLast([]candidate{resting, ready}, planned{order: make([]Weighed, 2)})
	if out[0].rest != "kimi@two" {
		t.Fatalf("no setting: the resting one was asked first: first %s", out[0].rest)
	}
	if pl.order[1].Keeps {
		t.Fatal("no setting: the trace says the pool keeps through it")
	}
}
