package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWellKnownMarkdownWithHTMLCommentIsInstalled(t *testing.T) {
	sandbox(t)
	const note = "<!-- Shared layout rules. -->\n# Layout\nKeep a clear hierarchy.\n"
	files := map[string]string{}
	files["/.well-known/skills/index.json"] = `{"skills":[{"name":"ui-taste","description":"UI taste","files":["SKILL.md","reference/layout.md"]}]}`
	files["/.well-known/skills/ui-taste/SKILL.md"] = websiteSkill
	files["/.well-known/skills/ui-taste/reference/layout.md"] = note
	base, _ := websiteFixture(t, files)
	if _, err := ProbeSkills(base); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallSkills(base, []string{"ui-taste"}, []string{}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(SkillPath("ui-taste"), "reference", "layout.md"))
	if err != nil || string(got) != note {
		t.Fatalf("the listed Markdown document must be installed unchanged: %q, %v", got, err)
	}
}

func TestWellKnownHTMLIsNotSkillMetadata(t *testing.T) {
	sandbox(t)
	files := map[string]string{}
	files["/.well-known/skills/index.json"] = `{"skills":[{"name":"ui-taste","description":"UI taste","files":["SKILL.md"]}]}`
	files["/.well-known/skills/ui-taste/SKILL.md"] = "<!doctype html><title>Not a skill</title>"
	base, _ := websiteFixture(t, files)
	if _, err := ProbeSkills(base); err == nil || !strings.Contains(err.Error(), "SKILL.md is invalid") {
		t.Fatalf("the metadata parser must reject an HTML landing page: %v", err)
	}
}
