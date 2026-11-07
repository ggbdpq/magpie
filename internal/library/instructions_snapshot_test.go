package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Library keeps a snapshot of the text it last wrote for Claude at
// instructions-claude.md beside library.json, exactly so magpie's own
// managed Claude spawns can carry it: they run with --setting-sources ""
// and never read ~/.claude/CLAUDE.md, so the Library could not deliver
// to the sessions magpie hosts (#5445, luzhuang). The snapshot follows
// the instructions: written when the shared text is on for claude,
// removed when it is off.
func TestInstructionsClaudeSnapshot(t *testing.T) {
	h := sandbox(t)
	snap := filepath.Join(Dir(), "instructions-claude.md")

	shared := "Be brief."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"claude"}}))
	if s := read(t, snap); !strings.Contains(s, "Be brief.") {
		t.Fatalf("snapshot after enabling:\n%s", s)
	}

	// a set switch carries the snapshot along
	work := "Work rules."
	ok(t)(SaveInstructions(InstructionsChange{Create: &InstrSet{ID: "work", Name: "Work"}, Texts: map[string]*string{"work": &work}, Activate: "work"}))
	if s := read(t, snap); !strings.Contains(s, "Work rules.") {
		t.Fatalf("snapshot after a set switch:\n%s", s)
	}

	// switched off for claude: the snapshot goes, the spawns read nothing
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"codex"}}))
	if _, err := os.Stat(snap); !os.IsNotExist(err) {
		t.Fatalf("snapshot kept after claude was switched off")
	}
	_ = h
}
