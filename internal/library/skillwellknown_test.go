package library

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// Uizze publishes a skills CLI index on its own website, rather than a
// git repository. The index lists each skill's complete set of files.
func TestWellKnownSourceInstallsAllFiles(t *testing.T) {
	sandbox(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/skills/index.json":
			fmt.Fprint(w, `{"skills":[{"name":"ui-taste","description":"UI taste","files":["SKILL.md","reference/craft.md"]},{"name":"ios-design","description":"iOS design","files":["SKILL.md"]}]}`)
		case "/.well-known/skills/ui-taste/SKILL.md":
			fmt.Fprint(w, "---\nname: ui-taste\ndescription: UI taste\n---\nRead reference/craft.md.\n")
		case "/.well-known/skills/ui-taste/reference/craft.md":
			fmt.Fprint(w, "Keep the product's visual hierarchy.")
		case "/.well-known/skills/ios-design/SKILL.md":
			fmt.Fprint(w, "---\nname: ios-design\ndescription: iOS design\n---\n")
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	input := "npx skills add " + srv.URL + "/"
	p, err := ProbeSkills(input)
	if err != nil {
		t.Fatalf("a website's well-known skills must be discoverable: %v", err)
	}
	if p.Kind != "well-known" || len(p.Candidates) != 2 {
		t.Fatalf("website skills: %+v", p)
	}
	res, err := InstallSkills(input, []string{"ui-taste"}, nil)
	if err != nil || len(res.Installed) != 1 || res.Installed[0] != "ui-taste" {
		t.Fatalf("install: %+v, %v", res, err)
	}
	if got := read(t, filepath.Join(skillDir("ui-taste"), "reference", "craft.md")); got != "Keep the product's visual hierarchy." {
		t.Fatalf("the skill's referenced file was not installed: %q", got)
	}
	l, err := load()
	if err != nil {
		t.Fatal(err)
	}
	s := l.skill("ui-taste")
	if s == nil || s.Source == nil || s.Source.Kind != "well-known" || s.Source.Repo != srv.URL || s.Source.Path != "ui-taste" || s.Hash == "" || s.Own == "" {
		t.Fatalf("the installed skill must retain its website source and hashes: %+v", s)
	}
}
