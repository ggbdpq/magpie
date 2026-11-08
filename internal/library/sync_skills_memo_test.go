package library

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
)

// Every realDir of every skill walks the same tree down to the agent's
// skills folder. With WSL detection on, that tree is a \\wsl.localhost
// path, where one look costs many of a local one (#5450): a sync stat'd
// the ancestors once per skill, and a folder of many skills took a
// minute-plus. Each agent's skill step now holds the page read's memo
// (linkmemo.go), so the ancestors are looked at once for the step, not
// once per skill.
func TestSyncSkillsLooksAtAncestorsOnce(t *testing.T) {
	sandbox(t)
	src := filepath.Join(t.TempDir(), "src")
	var names []string
	for i := range 32 {
		names = append(names, fmt.Sprintf("skill-%02d", i))
		write(t, filepath.Join(src, names[i], "SKILL.md"), "---\nname: "+names[i]+"\ndescription: a fixture\n---\nbody\n")
	}
	var ids, dirs []string
	for _, tg := range Targets() {
		ids = append(ids, tg.Agent.ID)
		if tg.Skills != "" && !slices.Contains(dirs, tg.Skills) {
			dirs = append(dirs, tg.Skills)
		}
	}
	ok(t)(InstallSkills(src, names, ids))

	home := os.Getenv("HOME")
	// the walks follow /var and kin through to where they point, so the
	// parts looked at are the resolved tree's
	if r, err := filepath.EvalSymlinks(home); err == nil {
		home = r
	}
	claudeSkills := filepath.Join(home, ".claude", "skills")
	if _, err := os.Stat(claudeSkills); err != nil {
		t.Fatalf("claude's skills folder: %v", err)
	}
	// the looks of the tree down to claude's skills folder, excluding the
	// skills themselves: the part every skill's walk repeats. A few walks
	// a sync makes once per agent (finding the targets, building the
	// steps); the storm is one walk per skill, so the bound is a few per
	// agent, far under one per skill.
	bound := int64(4*len(ids) + 8)
	var ancestorLooks atomic.Int64
	look := lookAt
	t.Cleanup(func() { lookAt = look })
	lookAt = func(p string) (part, bool) {
		if p == filepath.Join(home, ".claude") || p == claudeSkills {
			ancestorLooks.Add(1)
		}
		return look(p)
	}

	// a sync that gives the skills back: claude's folder is emptied first
	for _, n := range names {
		if err := os.RemoveAll(filepath.Join(claudeSkills, n)); err != nil {
			t.Fatal(err)
		}
	}
	ancestorLooks.Store(0)
	first, err := Sync()
	if err != nil {
		t.Fatal(err)
	}
	if n := ancestorLooks.Load(); n > bound {
		t.Errorf("the first sync looked at claude's ancestors %d times for %d skills (%d agents, bound %d); want a few per agent, not one walk per skill", n, len(names), len(ids), bound)
	}
	for _, n := range names {
		if _, err := os.Lstat(filepath.Join(claudeSkills, n)); err != nil {
			t.Fatalf("claude's %s after the sync: %v", n, err)
		}
	}

	// the sync again, nothing to change ("every agent already has it",
	// the report's own case): the same few looks, and nothing changed
	ancestorLooks.Store(0)
	second, err := Sync()
	if err != nil {
		t.Fatal(err)
	}
	if n := ancestorLooks.Load(); n > bound {
		t.Errorf("the second sync looked at claude's ancestors %d times for %d skills (%d agents, bound %d); want a few per agent, not one walk per skill", n, len(names), len(ids), bound)
	}
	if len(second.Changed) != 0 {
		t.Errorf("the second sync changed %v; want every agent already had it", second.Changed)
	}
	if len(first.Problems) > 0 || len(second.Problems) > 0 {
		t.Fatalf("problems: %+v %+v", first.Problems, second.Problems)
	}
}
