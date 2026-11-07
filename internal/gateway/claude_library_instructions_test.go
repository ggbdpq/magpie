package gateway

import (
	"strings"
	"testing"
)

// TestClaudeSpawnCarriesLibraryInstructions: the Library's text for Claude
// reaches the managed spawns, which run with --setting-sources "" and
// never read ~/.claude/CLAUDE.md, so the Library could not deliver to the
// very sessions magpie hosts (#5445, luzhuang). The sync keeps a snapshot
// at library/instructions-claude.md; the run carries it the way a caller's
// own instructions ride (--append-system-prompt-file) - alone on a run
// with no caller instructions of its own, before them when both are there.
// No snapshot, nothing extra; the caller's own path is untouched.
func TestClaudeSpawnCarriesLibraryInstructions(t *testing.T) {
	oldLib := claudeLibraryInstructions
	t.Cleanup(func() { claudeLibraryInstructions = oldLib })
	give := func(text string) { claudeLibraryInstructions = func() string { return text } }

	// a print run: the library text rides alone
	h, appended := appendHarness(t, false)
	give("Library rules.")
	h.post(`{"model":"claude-sonnet-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
	runs := appended()
	if len(runs) != 1 {
		t.Fatalf("%d runs, want 1: %v", len(runs), runs)
	}
	for log, content := range runs {
		if !strings.Contains(content, "Library rules.") {
			t.Fatalf("run %s carried no library text: %q", log, content)
		}
	}

	// gone: nothing extra is carried, and the caller's own path is
	// untouched (its system stays in the run as it was). A fresh harness:
	// each keeps its own copies.
	claudeLibraryInstructions = oldLib
	h2, appended2 := appendHarness(t, false)
	h2.post(`{"model":"claude-sonnet-5","max_tokens":64,"system":"caller rules.","messages":[{"role":"user","content":"hi again"}]}`)
	runs = appended2()
	if len(runs) != 1 {
		t.Fatalf("%d runs, want 1: %v", len(runs), runs)
	}
	for log, content := range runs {
		if strings.Contains(content, "Library rules.") {
			t.Fatalf("run %s carried a snapshot that is gone: %q", log, content)
		}
		if !strings.Contains(log, "caller rules.") {
			t.Fatalf("run %s lost the caller's own instructions:\n%s", log, content)
		}
	}

	// an interactive run with caller instructions: library first, caller last
	h3, appended3 := appendHarness(t, true)
	_ = h3
	give("Library rules.")
	h2.post(`{"model":"claude-sonnet-5","max_tokens":64,"system":"caller rules.","tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`)
	runs3 := appended3()
	if len(runs3) != 1 {
		t.Fatalf("%d runs, want 1: %v", len(runs3), runs3)
	}
	for log, content := range runs3 {
		lib, caller := strings.Index(content, "Library rules."), strings.Index(content, "caller rules.")
		if lib < 0 || caller < 0 || lib > caller {
			t.Fatalf("run %s: library text %d and caller text %d out of order in:\n%s", log, lib, caller, content)
		}
	}
}
