package provider

import (
	"testing"
	"time"
)

// CodexRoomKnown is whether what was read already says, plainly, that the
// ChatGPT account Codex is signed in to has room. CodexUsedUpKnown answers
// the takeover's taking on, which the absence of a known hold serves; the
// giving back needs more: a reading that failed, or none at all, is no
// answer, and one taken for room handed Codex back to a ChatGPT account
// still out while its app-server ran on magpie (#5316, zqbake; #5480,
// liu95270) — every thread of either provider then fails the app's
// provider check.
func TestCodexRoomKnown(t *testing.T) {
	signIn(t)
	rememberLogins(true)
	fresh := func() {
		loginUsageCache.Lock()
		loginUsageCache.m, loginUsageCache.pending = nil, nil
		loginUsageCache.Unlock()
	}
	keep := func(q SubscriptionQuota) {
		loginUsageCache.Lock()
		if loginUsageCache.m == nil {
			loginUsageCache.m = map[string]loginUsageEntry{}
		}
		loginUsageCache.m["codex/me@example.com"] = loginUsageEntry{at: time.Now(), q: q}
		loginUsageCache.Unlock()
	}

	fresh()
	// nothing read yet: no hold known, and no room either
	if CodexUsedUpKnown() {
		t.Fatal("CodexUsedUpKnown = true with nothing read")
	}
	if CodexRoomKnown() {
		t.Fatal("CodexRoomKnown = true with nothing read")
	}

	// held: the takeover's answer, and no room
	keep(SubscriptionQuota{Held: true})
	if !CodexUsedUpKnown() {
		t.Fatal("CodexUsedUpKnown = false on a held reading")
	}
	if CodexRoomKnown() {
		t.Fatal("CodexRoomKnown = true on a held reading")
	}

	// the vendor answered nothing: no hold known, and no room either
	keep(SubscriptionQuota{Error: "HTTP 432 Workspace routing is unavailable"})
	if CodexUsedUpKnown() {
		t.Fatal("CodexUsedUpKnown = true on a reading that failed")
	}
	if CodexRoomKnown() {
		t.Fatal("CodexRoomKnown = true on a reading that failed")
	}

	// room, plainly said
	keep(SubscriptionQuota{})
	if CodexUsedUpKnown() {
		t.Fatal("CodexUsedUpKnown = true on a room reading")
	}
	if !CodexRoomKnown() {
		t.Fatal("CodexRoomKnown = false on a room reading")
	}
}
