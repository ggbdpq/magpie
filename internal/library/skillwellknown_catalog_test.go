package library

import "testing"

func TestWellKnownCatalogListsOnlyItsEntries(t *testing.T) {
	sandbox(t)
	files := map[string]string{}
	files["/.well-known/skills/index.json"] = `{"skills":[{"name":"ui-taste","description":"UI taste","files":["SKILL.md","example-a/SKILL.md","example-b/SKILL.md"]}]}`
	files["/.well-known/skills/ui-taste/SKILL.md"] = websiteSkill
	files["/.well-known/skills/ui-taste/example-a/SKILL.md"] = "---\nname: example-a\ndescription: Example template\n---\n"
	files["/.well-known/skills/ui-taste/example-b/SKILL.md"] = "---\nname: example-b\ndescription: Example template\n---\n"
	base, _ := websiteFixture(t, files)
	p, err := ProbeSkills(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Candidates) != 1 || p.Candidates[0].Path != "ui-taste" {
		t.Fatalf("only entries declared by the website are installable; supporting examples are files: %+v", p.Candidates)
	}
}

func TestWellKnownGroupingKeepsWebsiteOrigin(t *testing.T) {
	for _, address := range []string{
		"http://localhost:8080/kit",
		"http://example.com:8080/kit",
		"http://example.com:8081/kit",
		"https://example.com:8443/Kit",
	} {
		t.Run(address, func(t *testing.T) {
			s := &Skill{Source: &Source{Kind: "well-known", Repo: address}}
			if got := (&tracer{}).repoOf(s); got != address {
				t.Fatalf("website grouping must keep the origin and path: got %q, want %q", got, address)
			}
		})
	}
}
