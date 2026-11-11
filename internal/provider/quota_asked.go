package provider

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/settings"
)

// Allowances read only when asked (#1518, RooobinYe): with Settings →
// Usage → Allowance reads at "When I ask", magpie asks no vendor for an
// account's allowance or a key's balance on its own — not for the Usage
// page's or tray's timed refresh, the menu bar, alerts, routing, or the
// switch to an account with room. Each shows the reading kept from the
// last time it was read, as of then (keepLast). A read the user asked
// for (Asked) goes out as before, and so does one of an account the
// vendor just turned away for its quota (StaleAllowance), which routing
// must not keep sending to.
//
// magpie started at login comes up before the proxy app does, and its own
// reads went out direct from the user's real address.

type askedKey struct{}

// Asked is ctx for a read the user asked for: the Usage page opened or
// refreshed, a card's refresh, `magpie quota`, `magpie accounts`.
func Asked(ctx context.Context) context.Context {
	return context.WithValue(ctx, askedKey{}, true)
}

func wasAsked(ctx context.Context) bool {
	b, _ := ctx.Value(askedKey{}).(bool)
	return b
}

// errNotAsked is what a read held back says: "not read yet", so keepLast
// gives the reading kept from the last time instead.
var errNotAsked = errors.New("not read yet: magpie reads allowances only when you ask (Settings → Usage)")

// readsAsked is whether the user has allowances read only when asked.
func readsAsked() bool { return settings.Load().QuotaReads == "asked" }

// heldRead is whether a read made with ctx is held back: the user has
// allowances read only when asked, and ctx is not one they asked for.
func heldRead(ctx context.Context) bool { return !wasAsked(ctx) && readsAsked() }

// holdUnasked is ctx, its requests held (netproxy.Hold) when it is a read
// held back: whatever a vendor's read goes through, nothing of it leaves.
func holdUnasked(ctx context.Context) context.Context {
	if netproxy.Held(ctx) == nil && heldRead(ctx) {
		return netproxy.Hold(ctx, errNotAsked)
	}
	return ctx
}

// staleReads are the accounts (agent/user) the vendor turned away for
// their quota since they were last read (StaleAllowance): read even while
// allowances are read only when asked.
var staleReads struct {
	sync.Mutex
	m map[string]bool
}

func markStaleRead(agent, user string) {
	staleReads.Lock()
	defer staleReads.Unlock()
	if staleReads.m == nil {
		staleReads.m = map[string]bool{}
	}
	staleReads.m[agent+"/"+strings.ToLower(user)] = true
}

// takeStaleRead says whether key is to be read though not asked, and
// forgets it: one read answers it.
func takeStaleRead(key string) bool {
	staleReads.Lock()
	defer staleReads.Unlock()
	if staleReads.m[key] {
		delete(staleReads.m, key)
		return true
	}
	return false
}

func isStaleRead(key string) bool {
	staleReads.Lock()
	defer staleReads.Unlock()
	return staleReads.m[key]
}

// carefulReasks are the accounts (agent/user) a careful provider
// (Provider.CapCareful) asked a fresh reading of to decide on, by the
// gateway's re-read (readCareful → ReadUsageNow): read for real even
// while allowances are read only when asked — StaleAllowance, which
// ReadUsageNow asks first, already says that — and, a saved Claude
// account's reading being otherwise never asked for (claudeWindows
// reads one only on its card's refresh, a switch's spare read, a hand
// refresh of every card, or the keep-alive, #5079), past that gate
// too. One real reading answers it (#5085).
var carefulReasks struct {
	sync.Mutex
	m map[string]bool
}

// CarefulReask asks that the account's next reading under agent go out
// for real, once, past the gates that would keep a saved account's
// reading to what it was: a careful provider holds an account it cannot
// decide on until its reading is fresh again, and nothing else was
// asking (#5079, #5085).
func CarefulReask(agent, user string) {
	carefulReasks.Lock()
	defer carefulReasks.Unlock()
	if carefulReasks.m == nil {
		carefulReasks.m = map[string]bool{}
	}
	carefulReasks.m[agent+"/"+strings.ToLower(user)] = true
}

// takeCarefulReask says whether key is a careful re-ask, and forgets it:
// one real reading answers it.
func takeCarefulReask(key string) bool {
	carefulReasks.Lock()
	defer carefulReasks.Unlock()
	if carefulReasks.m[key] {
		delete(carefulReasks.m, key)
		return true
	}
	return false
}

func isCarefulReask(key string) bool {
	carefulReasks.Lock()
	defer carefulReasks.Unlock()
	return carefulReasks.m[key]
}

// CarefulReasked says whether a careful re-ask of the account stands
// (planted, not yet answered by a reading).
func CarefulReasked(agent, user string) bool {
	return isCarefulReask(agent + "/" + strings.ToLower(user))
}

// holding is whether a read made with ctx sends nothing: held back
// (heldRead), or made under one that was (quotaReading put the hold on).
func holding(ctx context.Context) bool {
	return netproxy.Held(ctx) != nil || heldRead(ctx)
}

// unheld is ctx for a read that goes out though not asked for: an
// account the vendor turned away for its quota (StaleAllowance).
func unheld(ctx context.Context) context.Context {
	return netproxy.Hold(Asked(ctx), nil)
}
