package gateway

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// restart quits the gateway this harness drives and opens a new one, as
// magpie restarting does: the runs it had die with it, whatever its shelf
// kept and the sessions' files stay on disk. The shelf file is read as a
// new process reads it, having written none of it.
func (h *resumeHarness) restart() {
	h.t.Helper()
	h.s.subscription.abortAll()
	shelfFile.Lock()
	shelfFile.from, shelfFile.mod, shelfFile.last = "", time.Time{}, ""
	shelfFile.Unlock()
	h.s = New()
	h.t.Cleanup(h.s.subscription.abortAll)
}

// A conversation whose run was let go past idleMost goes on from its run's
// saved session after magpie restarts — its own update included — told the
// turn alone, where a restart lost the shelf and the conversation's next
// turn went to a new Claude Code from a transcript of it, a prefix the
// cache has never seen, the whole of it written again (#5971).
func TestClaudeShelfWaitsOutTheRestart(t *testing.T) {
	h := newResumeHarness(t)
	h.ask("rules A", "a1")
	var sidA string
	for pid := range h.runs() {
		sidA = "s" + pid
	}
	// let go past idleMost, A's session waits on the shelf
	for i := range idleMost {
		h.ask(fmt.Sprintf("rules %d", i), "x")
	}
	eventually(t, "A shelved", func() bool {
		h.s.subscription.mu.Lock()
		defer h.s.subscription.mu.Unlock()
		return len(h.s.subscription.shelf) == 1
	})
	h.restart()
	before := h.runs()
	h.ask("rules A", "a1", "a2")
	var resumed string
	for pid, log := range h.runs() {
		if _, ok := before[pid]; !ok {
			resumed = log
		}
	}
	if !strings.Contains(resumed, "--resume "+sidA) {
		t.Fatalf("A's next turn did not resume its session after the restart:\n%s", resumed)
	}
	told := resumed[strings.Index(resumed, "\ntold "):]
	if !strings.Contains(told, "a2") || strings.Contains(told, "a1") {
		t.Fatalf("the resumed run was told more than the turn:\n%s", told)
	}
}

// Every conversation that had a run waits for its next turn in a run of
// its own, none of them shelved while it waits. When magpie quits — a
// plain quit, its own update's handover included — the gateway ends, and
// each waiting run's session is shelved (shelveIdle, before the runs are
// aborted, as one let go past idleMost is), so the next turn of each goes
// on from the session it cached, not from a transcript of the whole
// conversation (#5971).
func TestClaudeWaitingRunsShelveForTheRestart(t *testing.T) {
	h := newResumeHarness(t)
	h.ask("rules A", "a1")
	var sidA string
	for pid := range h.runs() {
		sidA = "s" + pid
	}
	// the gateway ends: shelveIdle then abortAll, as its end does
	h.s.subscription.shelveIdle()
	h.s.subscription.abortAll()
	eventually(t, "A's session file left on disk", func() bool {
		return containsStr(h.sessions(), sidA)
	})
	h.restart()
	before := h.runs()
	h.ask("rules A", "a1", "a2")
	var resumed string
	for pid, log := range h.runs() {
		if _, ok := before[pid]; !ok {
			resumed = log
		}
	}
	if !strings.Contains(resumed, "--resume "+sidA) {
		t.Fatalf("A's next turn did not resume its session after the restart:\n%s", resumed)
	}
	told := resumed[strings.Index(resumed, "\ntold "):]
	if !strings.Contains(told, "a2") || strings.Contains(told, "a1") {
		t.Fatalf("the resumed run was told more than the turn:\n%s", told)
	}
}

// An entry of the shelf file past what its run would have waited
// (idleLongest) is dropped at the next start: the cache it was to read is
// gone either way, and the conversation's next turn is told whole, as it
// was before the shelf waited out the restart.
func TestClaudeRestartDropsStaleShelfEntries(t *testing.T) {
	h := newResumeHarness(t)
	h.ask("rules A", "a1")
	var sidA string
	for pid := range h.runs() {
		sidA = "s" + pid
	}
	h.s.subscription.shelveIdle()
	h.s.subscription.abortAll()
	// hours pass before magpie is opened again
	b, err := os.ReadFile(shelfPath())
	if err != nil {
		t.Fatalf("the shelf file: %v", err)
	}
	var saved map[string]savedShelf
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatalf("the shelf file: %v", err)
	}
	if len(saved) != 1 {
		t.Fatalf("the shelf file has %d entries", len(saved))
	}
	for k, v := range saved {
		v.At = time.Now().Add(-2 * idleLongest)
		saved[k] = v
	}
	b, err = json.Marshal(saved)
	if err != nil {
		t.Fatalf("the shelf file: %v", err)
	}
	os.WriteFile(shelfPath(), b, 0o600)
	shelfFile.Lock()
	shelfFile.from, shelfFile.mod, shelfFile.last = "", time.Time{}, ""
	shelfFile.Unlock()
	h.restart()
	before := h.runs()
	h.ask("rules A", "a1", "a2")
	for pid, log := range h.runs() {
		if _, ok := before[pid]; ok {
			continue
		}
		if strings.Contains(log, "--resume "+sidA) {
			t.Fatalf("the conversation went on from its session past what its run would have waited:\n%s", log)
		}
		told := log[strings.Index(log, "\ntold "):]
		if !strings.Contains(told, "a1") || !strings.Contains(told, "a2") {
			t.Fatalf("the stale conversation's next turn was not told the conversation whole:\n%s", told)
		}
	}
}

// The sweep at the next start leaves the sessions the shelf file keeps:
// each is newer than a session may be left to be swept, its entry within
// what its run would have waited. One the gateway that stopped long ago
// left beside it, past that, goes.
func TestClaudeSweepSparesShelvedSessions(t *testing.T) {
	h := newResumeHarness(t)
	h.ask("rules A", "a1")
	var sidA string
	for pid := range h.runs() {
		sidA = "s" + pid
	}
	h.s.subscription.shelveIdle()
	h.s.subscription.abortAll()
	// a session a gateway that stopped long ago left beside it
	projects, _ := filepath.Glob(filepath.Join(h.config, "projects", "*"))
	var dir string
	for _, d := range projects {
		if _, err := os.Stat(filepath.Join(d, sidA+".jsonl")); err == nil {
			dir = d
		}
	}
	if dir == "" {
		t.Fatalf("A's session file is gone: %v", h.sessions())
	}
	shelved := filepath.Join(dir, sidA+".jsonl")
	stale := filepath.Join(dir, "older.jsonl")
	os.WriteFile(stale, []byte("{}"), 0o600)
	long := time.Now().Add(-2 * idleLongest)
	os.Chtimes(stale, long, long)
	h.restart()
	if !containsStr(h.sessions(), sidA) {
		t.Fatalf("A's session file is gone after the restart: %v", h.sessions())
	}
	for _, d := range projects {
		h.s.subscription.sweepSessions([]string{d})
	}
	eventually(t, "the stale session swept", func() bool {
		_, err := os.Stat(stale)
		return os.IsNotExist(err)
	})
	// the sweep's pass over the folder's entries is done with the stale
	// one gone of it, and the shelved one a moment behind at most
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(shelved); err != nil {
		t.Fatalf("the shelved session was swept: %v", err)
	}
}
