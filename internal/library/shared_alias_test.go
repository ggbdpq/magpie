package library

import (
	"os"
	"path/filepath"
	"testing"
)

// WardLu (#6699): ~/.agents/skills a symlink to the library's own skills
// folder, as another skill manager's setups have. The library's entries
// then read as the user's own in the shared folder, so every agent that
// reads it (codex) is taken to have each already, ticked or not, and sync
// writes nothing into its own folder — while the alias shows it every
// skill in the library. The alias is not the user's shared folder: the
// ticks hold, and codex gets what it is ticked for in its own folder.
func TestSharedFolderIsTheLibrarysOwn(t *testing.T) {
	h := sandbox(t)
	noGoose(t, h)
	src := filepath.Join(h, "my-skills")
	for _, n := range []string{"alpha", "beta", "gamma"} {
		skill(t, filepath.Join(src, n), n, "The "+n)
	}
	ok(t)(InstallSkills(src, []string{"alpha", "beta", "gamma"}, []string{"claude"}))
	ok(t)(SkillAgents("gamma", []string{"claude", "codex"}))
	// the alias: ~/.agents/skills is the library's own folder
	if err := os.MkdirAll(filepath.Join(h, ".agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlink(t, skillsDir(), filepath.Join(h, ".agents/skills"))
	ok(t)(Sync())
	if !given(h, ".codex", "gamma") {
		t.Error("codex, ticked for gamma, has none of it")
	}
	if given(h, ".codex", "alpha") || given(h, ".codex", "beta") {
		t.Error("codex has a skill it isn't ticked for")
	}
	if !given(h, ".claude", "alpha") || !given(h, ".claude", "gamma") {
		t.Error("claude lost a skill it is ticked for")
	}
	// the library's own entries are none of them the user's to hand over
	if n, err := SharedSkills(); err != nil || len(n) != 0 {
		t.Errorf("the library's own skills are offered for hand-over: %v %v", n, err)
	}
}

// WardLu (#6697): with ~/.agents/skills the library's own folder, hand-
// over is offered for skills that are the library's, and takes one away:
// the entry that links to a folder kept elsewhere goes with the library's
// own, and the agent's link to it dangles. Nothing is handed over; the
// library stays as it was.
func TestHandOverIntoItsOwnFolder(t *testing.T) {
	h := sandbox(t)
	noGoose(t, h)
	ext := filepath.Join(h, "elsewhere/grilling")
	skill(t, ext, "grilling", "Grill")
	src := filepath.Join(h, "my-skills")
	skill(t, filepath.Join(src, "grilling"), "grilling", "Grill")
	skill(t, filepath.Join(src, "chop"), "chop", "Chop")
	ok(t)(InstallSkills(src, []string{"grilling", "chop"}, []string{"claude"}))
	// grilling's entry links to the folder kept elsewhere, as a folder
	// skill's does
	if err := os.RemoveAll(skillDir("grilling")); err != nil {
		t.Fatal(err)
	}
	symlink(t, ext, skillDir("grilling"))
	// the alias: ~/.agents/skills is the library's own folder
	if err := os.MkdirAll(filepath.Join(h, ".agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlink(t, skillsDir(), filepath.Join(h, ".agents/skills"))
	if _, err := HandOverSkills([]string{"grilling", "chop"}); err == nil {
		t.Fatal("hand-over took the library's own")
	}
	// the library is as it was
	if !linked(skillDir("grilling")) || realDir(skillDir("grilling")) != realDir(ext) {
		t.Error("grilling's entry in the library went")
	}
	if realDir(skillDir("chop")) != realDir(filepath.Join(src, "chop")) {
		t.Error("chop's entry in the library went")
	}
	if _, err := os.Stat(filepath.Join(ext, "SKILL.md")); err != nil {
		t.Error("the folder kept elsewhere went")
	}
	if _, err := os.Lstat(filepath.Join(h, ".claude/skills/grilling")); err != nil {
		t.Error("claude's own link to the folder elsewhere went")
	}
	// and none of it is offered any more
	if n, err := SharedSkills(); err != nil || len(n) != 0 {
		t.Errorf("the library's own skills are offered for hand-over: %v %v", n, err)
	}
}
