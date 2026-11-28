package provider

import (
	"fmt"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// #8059: a route group's regex ran against the provider's agent picker
// slice (the first 24 exposed models), not its whole servable catalog. With
// 29 matches spread over two relays the group only saw 16; moving one model
// from slot 24 to 25 flipped membership without changing the catalog's set.
func TestGroupPatternMatchesWholeVendorCatalog(t *testing.T) {
	patternHome(t)
	var a []catalog.Model
	for i := range 16 {
		a = append(a, catalog.Model{ID: fmt.Sprintf("other-%02d", i)})
	}
	for _, suffix := range []string{"argon", "carbon"} {
		for i := range 4 {
			a = append(a, catalog.Model{ID: fmt.Sprintf("gemini-4-%s-%02d", suffix, i)})
		}
	}
	for _, suffix := range []string{"hydrogen", "neon"} {
		n := 6
		if suffix == "hydrogen" {
			n = 7
		}
		for i := range n {
			a = append(a, catalog.Model{ID: fmt.Sprintf("gemini-4-%s-%02d", suffix, i)})
		}
	}
	if len(a) != 37 {
		t.Fatalf("synthetic relay-a catalog has %d models, want 37", len(a))
	}
	var b []catalog.Model
	for i := range 8 {
		b = append(b, catalog.Model{ID: fmt.Sprintf("gemini-4-standby-%02d", i)})
	}
	for _, p := range []struct {
		id, chat string
		models   []catalog.Model
	}{
		{"relay-a", "https://relay-a.example.invalid/v1", a},
		{"relay-b", "https://relay-b.example.invalid/v1", b},
	} {
		if err := Save(Provider{ID: p.id, Name: p.id, Key: "sk-test", Chat: p.chat, Unlisted: true}); err != nil {
			t.Fatal(err)
		}
		if err := catalog.SaveLive(p.id, p.chat, p.models); err != nil {
			t.Fatal(err)
		}
	}
	if err := SaveGroup(Group{Name: "gemini-4", Match: []string{`re:.*\/gemini(?:[-_. ])?4.*`}}); err != nil {
		t.Fatal(err)
	}
	g, _, ok := FindGroup("group/gemini-4")
	if !ok {
		t.Fatal("the group was not found")
	}
	if len(g.Matched) != 29 {
		t.Fatalf("group matched %d models, want all 29 regex matches (21 from relay-a, 8 from relay-b): %v", len(g.Matched), g.Matched)
	}
	for _, id := range []string{"relay-a/gemini-4-hydrogen-00", "relay-b/gemini-4-standby-00"} {
		if !slices.Contains(g.Matched, id) {
			t.Errorf("group missed %s", id)
		}
	}
}
