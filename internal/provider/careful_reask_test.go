package provider

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// miniersliu (#5085): a Claude subscription with two accounts signed in,
// both ticked — A Claude Code's own, B saved — both capped
// {"5 hours": 90}, capStop and capCareful on. B's reading is hours old
// at 30% of the window (after a restart, the disk's keepLast), and
// careful held B for days — "its usage couldn't be read", hundreds of
// rows a day — because the gateway's re-read (ReadUsageNow) never
// reached a saved account: its reading is asked for only by its card's
// refresh, a switch's spare read, a hand refresh of every card, or the
// keep-alive (#5079). CarefulReask is the ask the re-read plants, and a
// real reading answers it, at the same floor between readings as the
// account Claude Code is on.
func TestCarefulReaskReadsASavedClaudeAccount(t *testing.T) {
	claudeThree(t) // team@ and max@ saved, Claude Code on pro@
	setQuotaReads(t, "asked")
	t.Cleanup(func() { setQuotaReads(t, "") })
	saved := "max@x.com"

	// B's reading, as a restart leaves it: hours old, 30% of the window
	now := time.Now()
	claudeUsage.Lock()
	if claudeUsage.m == nil {
		claudeUsage.m = map[string]claudeUsageEntry{}
	}
	claudeUsage.m[strings.ToLower(saved)] = claudeUsageEntry{
		at: now.Add(-3 * time.Hour), tried: now.Add(-3 * time.Hour), whole: true,
		ws: []QuotaWindow{{Name: "Current session", Used: 30}, {Name: "Current week (all models)", Used: 12}},
	}
	claudeUsage.Unlock()
	t.Cleanup(func() {
		claudeUsage.Lock()
		delete(claudeUsage.m, strings.ToLower(saved))
		claudeUsage.Unlock()
		carefulReasks.Lock()
		carefulReasks.m = nil
		carefulReasks.Unlock()
	})

	// the resets /usage names, in the zone it names them in: the
	// session's inside its 5-hour span, the week's days out
	dubai, err := time.LoadLocation("Asia/Dubai")
	if err != nil {
		t.Fatal(err)
	}
	resetAt := func(d time.Duration) string { return now.In(dubai).Add(d).Format("Jan 2 at 3:04pm") }
	text := "Current session: 30% used · resets " + resetAt(2*time.Hour) + " (Asia/Dubai)\n" +
		"Current week (all models): 12% used · resets " + resetAt(72*time.Hour) + " (Asia/Dubai)"

	var mu sync.Mutex
	var dirs []string
	UsageClaudeVia(func(_ context.Context, dir string) (string, error) {
		mu.Lock()
		dirs = append(dirs, dir)
		mu.Unlock()
		return text, nil
	})
	t.Cleanup(func() {
		// nothing runs /usage once this test is done: the reads that
		// follow are the unset ones again (runClaudeUsage's own error)
		UsageClaudeVia(func(_ context.Context, _ string) (string, error) {
			return "", errClaudeCannotRun
		})
	})

	// the ask the gateway's careful re-read plants (readCareful)
	CarefulReask("claude", saved)
	if !ReadUsageNow(context.Background(), "claude", saved) {
		t.Fatal("the re-read came back with nothing")
	}
	mu.Lock()
	ran := slices.Clone(dirs)
	mu.Unlock()
	// B read for real, in its own directory; A held (asked only when
	// asked, nobody asked for it) and team@, saved and unasked, too
	if !slices.Equal(ran, []string{claudeAccountDir(saved)}) {
		t.Fatalf("/usage ran for %v, want only %s", ran, claudeAccountDir(saved))
	}
	at, failed, ok := UsageRead("claude", saved)
	if !ok || failed || time.Since(at) >= claudeAskFloor {
		t.Fatalf("B's reading after the re-read: at %v failed %v ok %v", at, failed, ok)
	}
	al, known := Allowances("claude")[saved]
	if !known {
		t.Fatal("B's allowance is missing after the re-read")
	}
	used := 0.0
	for _, l := range al {
		used = max(used, l.Used)
	}
	if used != 30 {
		t.Fatalf("B reads %.0f%% used, want the fresh 30%%: %+v", used, al)
	}

	// the floor between readings holds for the careful ask too: a second
	// ask at once is answered by the reading just taken, not the vendor
	CarefulReask("claude", saved)
	if !ReadUsageNow(context.Background(), "claude", saved) {
		t.Fatal("the second re-read came back with nothing")
	}
	mu.Lock()
	ran = slices.Clone(dirs)
	mu.Unlock()
	if !slices.Equal(ran, []string{claudeAccountDir(saved)}) {
		t.Fatalf("/usage ran again inside the floor: %v", ran)
	}
}
