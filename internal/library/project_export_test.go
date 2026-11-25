package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A project's choices taken to another folder with a library of its own
// (#7278): its hooks and subagents are saved there from the export, given
// to the new folder by the same writers, beside what the folder has; what
// the other library lacks is named, and applying again changes nothing.
// None of the first folder's ownership records goes along.
func TestProjectExportApply(t *testing.T) {
	_, a, _ := pluginProjects(t)
	ok(t)(SaveHook("", Hook{Name: "bark", Event: "session-start", Command: "say bark", Agents: []string{}}))
	s := reviewer
	s.Name, s.Agents, s.Effort = "checker", []string{}, "low"
	ok(t)(SaveSubagent("", s))
	ok(t)(ProjectHooks(a, []string{"bark"}, []string{"claude"}))
	ok(t)(ProjectSubagents(a, []string{s.Name}, []string{"claude"}))
	ok(t)(ProjectPlugin(a, "acme-kit", []string{"claude"}))
	ok(t)(ProjectPi(a, "npm:pi-demo", "on"))

	e, err := ExportProject(a)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, own := range []string{"placed", "hookRecs", "agentFiles", "piRecs", "made", "wrote"} {
		if strings.Contains(string(b), `"`+own+`"`) {
			t.Errorf("the export carries %s: %s", own, b)
		}
	}
	if !strings.Contains(string(b), "say bark") || !strings.Contains(string(b), "You review code.") {
		t.Errorf("the hook's and subagent's definitions didn't go: %s", b)
	}

	// another computer: a library of its own, a folder of its own with
	// the user's hook in it, and none of the first folder
	h := sandbox(t)
	pluginAgents(t, h)
	var back ProjectExport
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	c := filepath.Join(t.TempDir(), "c")
	gitInit(t, c)
	f := filepath.Join(c, ".claude/settings.local.json")
	write(t, f, `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "echo mine"}]}]}}`)
	if err := os.RemoveAll(a); err != nil {
		t.Fatal(err)
	}

	out, err := ApplyProject(c, &back)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(out.Saved, []string{"hook bark", "subagent " + s.Name}) {
		t.Errorf("saved %q", out.Saved)
	}
	if len(out.Missing) != 1 || !strings.HasPrefix(out.Missing[0], "plugin acme-kit") {
		t.Errorf("missing %q", out.Missing)
	}
	for _, r := range out.Res {
		if len(r.Problems) > 0 {
			t.Errorf("problems: %+v", r.Problems)
		}
	}
	l := loadT(t)
	if x := l.subagent(s.Name); x == nil || x.Effort != "low" {
		t.Errorf("subagent's effort didn't go: %+v", x)
	}
	if l.hook("bark") == nil || len(l.hook("bark").Agents) != 0 {
		t.Errorf("hook: %+v", l.hook("bark"))
	}
	if p := l.project(c); p == nil || !slices.Equal(p.Hooks["bark"], []string{"claude"}) || !slices.Equal(p.Subagents[s.Name], []string{"claude"}) {
		t.Fatalf("project: %+v", p)
	}
	got := read(t, f)
	if !strings.Contains(got, "say bark") || !strings.Contains(got, "echo mine") {
		t.Errorf("settings.local.json:\n%s", got)
	}
	there(t, filepath.Join(c, ".claude/agents", s.Name+".md"))
	if pi := read(t, filepath.Join(c, ".pi/settings.json")); strings.Count(pi, "npm:pi-demo") != 1 {
		t.Errorf(".pi/settings.json:\n%s", pi)
	}

	// again: nothing more
	out, err = ApplyProject(c, &back)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Saved) != 0 || len(out.Missing) != 1 {
		t.Errorf("again: saved %q, missing %q", out.Saved, out.Missing)
	}
	if n := strings.Count(read(t, f), "say bark"); n != 1 || !strings.Contains(read(t, f), "echo mine") {
		t.Errorf("again, %d entries:\n%s", n, read(t, f))
	}
	if pi := read(t, filepath.Join(c, ".pi/settings.json")); strings.Count(pi, "npm:pi-demo") != 1 {
		t.Errorf("again, .pi/settings.json:\n%s", pi)
	}
	if _, err := ApplyProject(c, &ProjectExport{Kind: "other"}); err == nil {
		t.Error("a file that isn't an export was applied")
	}
}

// #7682 (Arobotmaster): project export carries MainOnly and ApplyProject
// enables it before the first resource write so linked worktrees are never
// touched. An older or default export that omits mainOnly preserves an
// existing destination's main-only policy.
func TestProjectExportApplyMainOnly(t *testing.T) {
	h := sandbox(t)
	src := filepath.Join(h, "skills-src")
	skill(t, filepath.Join(src, "marker"), "marker", "Marker skill")
	skill(t, filepath.Join(src, "extra"), "extra", "Extra skill")
	ok(t)(InstallSkills(src, []string{"marker", "extra"}, []string{}))

	addWorktree := func(repo, name string) string {
		t.Helper()
		wt := filepath.Join(filepath.Dir(repo), name)
		own := filepath.Join(repo, ".git", "worktrees", name)
		write(t, filepath.Join(wt, ".git"), "gitdir: "+own+"\n")
		write(t, filepath.Join(own, "gitdir"), filepath.Join(wt, ".git")+"\n")
		write(t, filepath.Join(own, "commondir"), "../..\n")
		return wt
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	gitInit(t, source)
	sourceWT := addWorktree(source, "source-wt")

	dest := filepath.Join(root, "destination")
	gitInit(t, dest)
	destWT := addWorktree(dest, "destination-wt")

	ok(t)(AddProject(source))
	ok(t)(ProjectMainOnly(source, true))
	ok(t)(ProjectSkills(source, []string{"marker"}, []string{"codex"}))
	if !isLink(t, filepath.Join(source, ".agents", "skills", "marker")) {
		t.Fatal("source didn't get marker")
	}
	gone(t, filepath.Join(sourceWT, ".agents", "skills", "marker"))

	e, err := ExportProject(source)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"mainOnly":true`) {
		t.Errorf("export omits mainOnly: %s", b)
	}

	var back ProjectExport
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	out, err := ApplyProject(dest, &back)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Missing) != 0 {
		t.Errorf("missing: %q", out.Missing)
	}
	for _, r := range out.Res {
		if len(r.Problems) > 0 {
			t.Errorf("problems: %+v", r.Problems)
		}
	}
	if !isLink(t, filepath.Join(dest, ".agents", "skills", "marker")) {
		t.Error("destination didn't get marker")
	}
	gone(t, filepath.Join(destWT, ".agents", "skills", "marker"))
	gone(t, filepath.Join(destWT, ".agents"))

	findProj := func(dir string) *ProjectView {
		t.Helper()
		v, err := Read(nil)
		if err != nil {
			t.Fatal(err)
		}
		for i := range v.Projects {
			if v.Projects[i].Dir == dir {
				return &v.Projects[i]
			}
		}
		return nil
	}
	if pv := findProj(dest); pv == nil || !pv.MainOnly || len(pv.Trees) != 0 {
		t.Errorf("destination view: %+v", pv)
	}

	// applying again is a no-op and keeps main-only
	if _, err := ApplyProject(dest, &back); err != nil {
		t.Fatal(err)
	}
	gone(t, filepath.Join(destWT, ".agents", "skills", "marker"))

	// an older export that omits mainOnly preserves destination's main-only policy
	var older ProjectExport
	if err := json.Unmarshal([]byte(`{"kind":"magpie-project","version":1,"skills":{"extra":["codex"]},"need":{"skills":{"extra":""}}}`), &older); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyProject(dest, &older); err != nil {
		t.Fatal(err)
	}
	if !isLink(t, filepath.Join(dest, ".agents", "skills", "extra")) {
		t.Error("destination didn't get extra from older export")
	}
	gone(t, filepath.Join(destWT, ".agents", "skills", "extra"))
	if pv := findProj(dest); pv == nil || !pv.MainOnly || len(pv.Trees) != 0 {
		t.Errorf("destination lost mainOnly after older export: %+v", pv)
	}

	// a default export (main-only off) omits mainOnly and does not clear destination's policy
	plain := filepath.Join(root, "plain")
	gitInit(t, plain)
	ok(t)(AddProject(plain))
	ok(t)(ProjectSkills(plain, []string{"extra"}, []string{"codex"}))
	pe, err := ExportProject(plain)
	if err != nil {
		t.Fatal(err)
	}
	pb, err := json.Marshal(pe)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(pb), `"mainOnly"`) {
		t.Errorf("default export unexpectedly carries mainOnly: %s", pb)
	}
	if _, err := ApplyProject(dest, pe); err != nil {
		t.Fatal(err)
	}
	gone(t, filepath.Join(destWT, ".agents", "skills", "extra"))
	if pv := findProj(dest); pv == nil || !pv.MainOnly {
		t.Errorf("destination lost mainOnly after default export: %+v", pv)
	}

	// an existing destination with mirroring on switches to main-only and clears worktrees
	existing := filepath.Join(root, "existing")
	gitInit(t, existing)
	existingWT := addWorktree(existing, "existing-wt")
	ok(t)(AddProject(existing))
	ok(t)(ProjectSkills(existing, []string{"extra"}, []string{"codex"}))
	if !isLink(t, filepath.Join(existingWT, ".agents", "skills", "extra")) {
		t.Fatal("existing worktree wasn't mirrored before apply")
	}
	if _, err := ApplyProject(existing, &back); err != nil {
		t.Fatal(err)
	}
	if !isLink(t, filepath.Join(existing, ".agents", "skills", "marker")) || !isLink(t, filepath.Join(existing, ".agents", "skills", "extra")) {
		t.Error("existing destination missing skills after apply")
	}
	gone(t, filepath.Join(existingWT, ".agents", "skills", "extra"))
	gone(t, filepath.Join(existingWT, ".agents", "skills", "marker"))
	if pv := findProj(existing); pv == nil || !pv.MainOnly || len(pv.Trees) != 0 {
		t.Errorf("existing destination view after apply: %+v", pv)
	}

	// main-only takes effect before the first resource write: a fresh
	// destination whose linked worktree already has a user-owned marker
	// folder reports no collision problem and leaves it untouched
	guarded := filepath.Join(root, "guarded")
	gitInit(t, guarded)
	guardedWT := addWorktree(guarded, "guarded-wt")
	ownMarker := filepath.Join(guardedWT, ".agents", "skills", "marker", "SKILL.md")
	write(t, ownMarker, "worktree own\n")
	out, err = ApplyProject(guarded, &back)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range out.Res {
		if len(r.Problems) > 0 {
			t.Errorf("apply touched linked worktree before enabling main-only: %+v", r.Problems)
		}
	}
	if !isLink(t, filepath.Join(guarded, ".agents", "skills", "marker")) {
		t.Error("guarded destination didn't get marker")
	}
	if got := read(t, ownMarker); got != "worktree own\n" {
		t.Errorf("worktree's own marker changed: %q", got)
	}
}
