package gui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
)

// jackyfan01 (#5627): 「为你调优」 was blank for Qoder with calls to
// spare, and said "not enough calls" — not why. Tune read only claude and
// codex, two names three places of the gui carried each on its own, and
// an agent with no setting of tune's has no card for its calls to earn.
// The names come from agent.TuneAgents now, and the state says why an
// agent is blank: no setting of tune's (Others), or files magpie may not
// read (Unread).
func TestTuneTellsWhyAnAgentIsBlank(t *testing.T) {
	h := sandboxHome(t)
	// a test before this one may have left the consent written: reads as
	// this test starts are magpie's own default, everything read
	if err := settings.SetSessionReads(func(*settings.SessionReads) *settings.SessionReads { return nil }); err != nil {
		t.Fatal(err)
	}
	sessions.Reset()
	t.Cleanup(func() {
		settings.SetSessionReads(func(*settings.SessionReads) *settings.SessionReads { return nil })
		sessions.Reset()
	})
	home := t.TempDir()
	qoder := tuneCalls("qoder-cn", "qoder-max", 40)
	st := tuneOf(qoder, 1, home)
	if len(st.Agents) != 0 {
		t.Fatalf("qoder-cn has no setting of tune's: agents %+v", st.Agents)
	}
	if len(st.Others) != 1 || st.Others[0] != "qoder-cn" {
		t.Fatalf("qoder-cn's calls with no setting of tune's: others %+v; want qoder-cn", st.Others)
	}
	if len(st.Unread) != 0 {
		t.Fatalf("nothing keeps magpie from reading here: unread %+v", st.Unread)
	}
	// Claude Code's calls beside Qoder's make its card; Qoder's note stays
	st = tuneOf(append(qoder, tuneCalls("claude", "claude-opus-4-5", 80)...), 1, home)
	if len(st.Agents) != 1 || st.Agents[0].Agent != "claude" || len(st.Agents[0].Advice) == 0 {
		t.Fatalf("claude's calls: agents %+v; want claude's card", st.Agents)
	}
	if len(st.Others) != 1 || st.Others[0] != "qoder-cn" {
		t.Fatalf("claude's card and qoder-cn's note: others %+v; want qoder-cn", st.Others)
	}
	// the production route: the answer names the agent of the range it has
	// no card for, from the sessions page's listing — callSources, what
	// the calls are read from, has none of Qoder's files
	proj := filepath.Join(h, ".qoder-cn", "projects", "-work-app")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","timestamp":"` + time.Now().UTC().Format("2006-01-02T15:04:05Z") + `","message":{"role":"user","content":"hi"},"cwd":"/work/app","sessionId":"s1"}`
	if err := os.WriteFile(filepath.Join(proj, "sess1.jsonl"), []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sessions.Reset()
	if _, ok := sessions.TouchedSince(time.Now().Add(-24 * time.Hour))["qoder-cn"]; !ok {
		t.Fatal("qoder-cn's file touched since, yet the listing has none")
	}
	var answer tuneState
	getJSON(t, keptMux(t), "/api/tune?days=1", &answer)
	if len(answer.Others) != 1 || answer.Others[0] != "qoder-cn" {
		t.Fatalf("/api/tune: others %+v; want qoder-cn", answer.Others)
	}
	// an agent with a store here that magpie may not read is blank for
	// that, not for being few: Codex's folder is here, the consent off
	if err := os.MkdirAll(filepath.Join(h, ".codex", "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := settings.SetSessionReads(func(*settings.SessionReads) *settings.SessionReads {
		return &settings.SessionReads{}
	}); err != nil {
		t.Fatal(err)
	}
	sessions.Reset()
	t.Cleanup(func() {
		settings.SetSessionReads(func(*settings.SessionReads) *settings.SessionReads { return nil })
		sessions.Reset()
	})
	st = tuneOf(tuneCalls("claude", "claude-opus-4-5", 80), 1, home)
	if len(st.Agents) != 1 || st.Agents[0].Agent != "claude" {
		t.Fatalf("claude's calls: agents %+v; want claude's card alone", st.Agents)
	}
	if len(st.Unread) != 1 || st.Unread[0] != "codex" {
		t.Fatalf("codex here, reads off: unread %+v; want codex", st.Unread)
	}
	if len(st.Others) != 0 {
		t.Fatalf("no qoder call any more: others %+v", st.Others)
	}
}

// The names and the places can't drift apart: every agent TuneAgents
// names has a place tuneWhere reads a setting from, and Qoder, whose
// sessions magpie reads as Claude Code's are, has none — that is why it
// had no card, and the pane has to say so, not count its calls as few
// (#5627).
func TestTuneAgentsHavePlaces(t *testing.T) {
	sandboxHome(t)
	home := t.TempDir()
	for _, name := range agent.TuneAgents() {
		if _, err := agent.TuneRead(home, name, agent.TuneCompact, "claude-opus-4-5"); err != nil {
			t.Fatalf("%s is a name of tune's, yet no place for its window: %v", name, err)
		}
	}
	if agent.Tunable("qoder-cn") {
		t.Fatal("qoder-cn has no setting of tune's, yet Tunable says it has")
	}
}
