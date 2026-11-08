package provider

import (
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// The provider editor's Names, prices & levels are made with its Save, all at
// once: each is made as SetModelName, SetModelEfforts and SetModelImage
// make it, and the agents' files are rewritten once, not once a change
// (ARNO on Discord: every reasoning level ticked was applied on its own,
// so picking them lagged).
func TestSetModelPrefs(t *testing.T) {
	prefsHome(t)
	touched := 0
	catalog.Changed = func() { touched++ }
	t.Cleanup(func() { catalog.Changed = nil })
	name, yes := "My Sol", true
	efforts := []string{"low", "high"}
	if err := SetModelPrefs("a", map[string]ModelPref{"sol": {Name: &name, Efforts: &efforts, Images: &yes}}); err != nil {
		t.Fatal(err)
	}
	if touched != 1 {
		t.Fatalf("agents told %d times, want once", touched)
	}
	s := settings.Load()
	if s.ModelNames["a/sol"] != "My Sol" || !slices.Equal(s.ModelEfforts["a/sol"], efforts) || !s.ModelImages["a/sol"] {
		t.Fatalf("names %v, efforts %v, images %v", s.ModelNames, s.ModelEfforts, s.ModelImages)
	}
	if _, ok := s.ModelNames["b/sol"]; ok {
		t.Fatal("b's sol was named too")
	}
	// nothing changed: nothing to tell
	if err := SetModelPrefs("a", map[string]ModelPref{"sol": {Name: &name}}); err != nil || touched != 1 {
		t.Fatalf("%v, told %d times", err, touched)
	}
	// Restore default: its own name, every level, the vendor's answer
	own, all := "", []string{}
	if err := SetModelPrefs("a", map[string]ModelPref{"sol": {Name: &own, Efforts: &all, OwnImages: true}}); err != nil {
		t.Fatal(err)
	}
	s = settings.Load()
	if len(s.ModelNames)+len(s.ModelEfforts)+len(s.ModelImages) != 0 || touched != 2 {
		t.Fatalf("names %v, efforts %v, images %v, told %d times", s.ModelNames, s.ModelEfforts, s.ModelImages, touched)
	}
	// a level it hasn't is refused, the change before it still told
	bad := []string{"xhigh"}
	err := SetModelPrefs("a", map[string]ModelPref{"sol": {Name: &name, Efforts: &bad}})
	if err == nil || !strings.Contains(err.Error(), "xhigh") {
		t.Fatalf("got %v", err)
	}
	if settings.Load().ModelNames["a/sol"] != "My Sol" || touched != 3 {
		t.Fatalf("names %v, told %d times", settings.Load().ModelNames, touched)
	}
}

// An override that names every level the model has is kept all the same
// (#5458): folding it away as "offered all again" left no override in
// settings, and a model whose levels the gateway doesn't publish (its
// vendor's list is silent about them) then had no supported way to be
// told them — the CLI said the levels, the list published none, and
// setting exactly those levels changed nothing. What the user gave is
// kept now; only a reset (no levels) offers them all again by taking
// the override away.
func TestModelEffortsNamingAllLevelsAreKept(t *testing.T) {
	prefsHome(t)
	p, err := Find("a")
	if err != nil {
		t.Fatal(err)
	}
	all := p.Known("sol")
	if want := []string{"low", "medium", "high", "max"}; !slices.Equal(all, want) {
		t.Fatalf("Known(sol) = %v, want %v", all, want)
	}
	// a subset was kept before (#92) and still is
	if err := SetModelEfforts("a/sol", []string{"low", "high"}); err != nil {
		t.Fatal(err)
	}
	if kept := settings.Load().ModelEfforts["a/sol"]; !slices.Equal(kept, []string{"low", "high"}) {
		t.Fatalf("subset kept as %v", kept)
	}
	// the whole native set, spelt by the user: kept, not folded away
	if err := SetModelEfforts("a/sol", []string{"max", "high", "medium", "low"}); err != nil {
		t.Fatal(err)
	}
	kept, ok := settings.Load().ModelEfforts["a/sol"]
	if !ok || !slices.Equal(kept, all) {
		t.Fatalf("an override equal to the native set was not kept: %v", settings.Load().ModelEfforts)
	}
	// and the catalog carries it, in the vendor's order
	if got := entry(t, "a/sol").Efforts; !slices.Equal(got, all) {
		t.Fatalf("entry efforts %v, want %v", got, all)
	}
	// setting the same set again changes nothing further (told once more
	// here, as the set above was a change)
	if err := SetModelEfforts("a/sol", []string{"low", "medium", "high", "max"}); err != nil {
		t.Fatal(err)
	}
	if kept := settings.Load().ModelEfforts["a/sol"]; !slices.Equal(kept, all) {
		t.Fatalf("kept as %v after setting the same set again", kept)
	}
	// a reset still takes the override away
	if err := SetModelEfforts("a/sol", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := settings.Load().ModelEfforts["a/sol"]; ok {
		t.Fatal("reset left the override behind")
	}
	if got := entry(t, "a/sol").Efforts; !slices.Equal(got, all) {
		t.Fatalf("after a reset the entry offers %v, want all of %v", got, all)
	}
}
