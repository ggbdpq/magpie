package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/usage"
)

// The Claude Code runs magpie started itself — the subscription bridge's,
// a warm-up's — write the user's history.jsonl as they go, and none of
// their requests reach the gateway as agent claude's (the bridge's go as
// claude-desktop's, a warm-up's straight to Anthropic): drift would take
// them for the user using Claude Code behind magpie's back (#5100). A
// session magpie started is not use; one the user started is.
func TestClaudeDriftOwnSessions(t *testing.T) {
	home, _ := codexHome(t, "", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	managed := claudeManaged
	claudeManaged = func() string { return filepath.Join(home, "managed-settings.json") }
	t.Cleanup(func() { claudeManaged = managed })
	cl := claude(home)
	defer func(s time.Time) { started = s }(started)
	started = time.Now().Add(-time.Hour)
	// a request reaching the gateway, kept to this run: usage.Saw's record
	// lasts the process, so the next run (-count=2) would start with it
	var saw time.Time
	defer func(f func(string) time.Time) { lastSeen = f }(lastSeen)
	lastSeen = func(string) time.Time { return saw }
	if err := cl.Apply("model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	// Apply stamped now; put it back to before the prompts
	m := appliedLoad()
	a := m["claude"]
	a.At = time.Now().Add(-10 * time.Minute)
	m["claude"] = a
	appliedSave(m)
	hist := filepath.Join(home, ".claude", "history.jsonl")
	line := func(entries ...map[string]any) {
		var b []byte
		for _, e := range entries {
			j, _ := json.Marshal(e)
			b = append(b, j...)
			b = append(b, '\n')
		}
		if err := os.WriteFile(hist, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	prompt := func(sid, display, project string, at time.Time) map[string]any {
		return map[string]any{"display": display, "timestamp": at.UnixMilli(), "project": project, "sessionId": sid}
	}
	work := filepath.Join(os.TempDir(), "magpie-claude-work-1000")
	proj := filepath.Join(home, "work", "bdr")
	// a warm-up's "hi", the log's newest line: its run asked Anthropic
	// straight, so the gateway saw nothing — not the user's use
	usage.OwnSession("warmup-sid")
	line(prompt("warmup-sid", "hi", work, time.Now().Add(-time.Minute)))
	if d := cl.Drift(); d != nil {
		t.Fatalf("magpie's own warm-up: %+v", d)
	}
	// the bridge's session, on the user's own project — no magpie folder in
	// its path to go by: the session magpie started is what says it isn't use
	usage.OwnSession("bridge-sid")
	line(prompt("bridge-sid", "[Image #6][Pasted text #7 +3 lines]", proj, time.Now().Add(-time.Minute)))
	if d := cl.Drift(); d != nil {
		t.Fatalf("magpie's own bridge session: %+v", d)
	}
	// the user's own use, none of it at the gateway: bypassed
	used := time.Now().Add(-5 * time.Minute)
	line(prompt("user-sid", "hi", proj, used))
	if d := cl.Drift(); d == nil || d.Kind != "bypassed" {
		t.Fatalf("the user's use: %+v", d)
	}
	// a run of magpie's own above the user's line: the use said is still
	// the user's, at the user's time
	usage.OwnSession("later-sid")
	line(prompt("user-sid", "hi", proj, used), prompt("later-sid", "hi", work, time.Now().Add(-time.Minute)))
	if d := cl.Drift(); d == nil || d.Kind != "bypassed" || !strings.Contains(d.Detail, "used at "+used.Format("15:04")) {
		t.Fatalf("a run of magpie's own above the user's: %+v", d)
	}
}
