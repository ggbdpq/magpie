package library

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// hieuthahn (yetone/magpie#6818): every extra Claude Code config folder's
// CLAUDE.md linked to ~/.claude/CLAUDE.md, so every account reads the same
// instructions. Library → Instructions had claude on and the claude# rows
// off. claude wrote its part, and the unticked claude# row, wanting
// nothing, read it through the link and took it out again, leaving
// ~/.claude/CLAUDE.md empty. One switched on keeps the file it shares
// through a link with one switched off, as with a file two agents read in
// the same place (TestAntigravitySharedInstructionsFile); the one off
// doesn't call the part "edited"; a file an old write left empty is
// written again by the next sync; with both on, the file keeps one part;
// and both off takes it out, as on the page.
func TestClaudeProfileSharesLinkedInstructionsFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a file link has no stand-in a test may make (a junction is a folder's)")
	}
	h := sandbox(t)
	dir := filepath.Join(h, ".claude-acct")
	write(t, filepath.Join(dir, "settings.json"), "")
	p := filepath.Join(h, ".claude", "CLAUDE.md")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(p, filepath.Join(dir, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	if tg := targetByID("claude#acct"); tg == nil || tg.Instructions != filepath.Join(dir, "CLAUDE.md") {
		t.Fatalf("claude#acct: %+v", tg)
	}
	want := blockBegin + "\nUse tabs.\n" + blockEnd + "\n"
	shared := "Use tabs."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"claude"}}))
	if s := read(t, p); s != want {
		t.Fatalf("after the switch the claude# row took the part out of the linked file:\n%q", s)
	}
	ok(t)(Sync())
	if s := read(t, p); s != want {
		t.Fatalf("after a sync:\n%q", s)
	}
	v, err := ReadInstructions()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range v.Agents {
		if a.Agent == "claude#acct" && (a.Edited || a.On) {
			t.Errorf("row %+v", a)
		}
		if a.Agent == "claude" && (a.Edited || !a.On) {
			t.Errorf("row %+v", a)
		}
	}

	// as an old write left it: the part taken out, the file empty
	write(t, p, "")
	ok(t)(Sync())
	if s := read(t, p); s != want {
		t.Fatalf("a sync didn't write the emptied file again:\n%q", s)
	}

	// both on: the file keeps one part, over two syncs
	ok(t)(SaveInstructions(InstructionsChange{Agents: []string{"claude", "claude#acct"}}))
	ok(t)(Sync())
	if s := read(t, p); s != want {
		t.Fatalf("with both on:\n%q", s)
	}
	ok(t)(Sync())
	if s := read(t, p); s != want {
		t.Fatalf("with both on, a second sync:\n%q", s)
	}

	// both off: the part goes
	ok(t)(SaveInstructions(InstructionsChange{Agents: []string{}}))
	if s := read(t, p); s != "" {
		t.Errorf("both off:\n%q", s)
	}
}
