package provider

import (
	"maps"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// A threshold too small to hold an agent's system prompt (#4148: 400 typed
// for 400K) is refused, for every model and for one provider's, with the
// size it was probably meant as; what was saved stays, and catalog.LeastWindow
// itself, the smallest window magpie takes a list at its word for, is
// accepted. A small value saved before the floor and sent back as it is
// stays, as a model the provider no longer lists does (#3305).
func TestCompactAtTooSmallIsRefused(t *testing.T) {
	limitsHome(t)
	if err := settings.Save(settings.Settings{CompactAt: 500000}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{1, 400, catalog.LeastWindow - 1} {
		err := SetCompactAt(n)
		if err == nil {
			t.Errorf("SetCompactAt(%d) was accepted", n)
			continue
		}
		if n == 400 && !strings.Contains(err.Error(), "400k") {
			t.Errorf("SetCompactAt(400): %q doesn't say 400k", err)
		}
	}
	if s := settings.Load(); s.CompactAt != 500000 {
		t.Errorf("a refused size changed it: %d", s.CompactAt)
	}
	if err := SetCompactAt(catalog.LeastWindow); err != nil || settings.Load().CompactAt != catalog.LeastWindow {
		t.Errorf("SetCompactAt(LeastWindow): %v, saved %d", err, settings.Load().CompactAt)
	}
	if err := SetCompactAt(0); err != nil || settings.Load().CompactAt != 0 {
		t.Errorf("0 (the working window): %v, saved %d", err, settings.Load().CompactAt)
	}

	if err := Save(Provider{ID: "deepseek", Name: "deepseek", Chat: "https://relay.example/v1", Key: "k", Models: []string{"deepseek-v4-flash"}}); err != nil {
		t.Fatal(err)
	}
	if err := SetModelCompacts("deepseek", map[string]int{"*": 500000}); err != nil {
		t.Fatal(err)
	}
	for _, cs := range []map[string]int{{"*": 400}, {"*": 500000, "deepseek-v4-flash": 400}} {
		if err := SetModelCompacts("deepseek", cs); err == nil {
			t.Errorf("SetModelCompacts(%v) was accepted", cs)
		}
	}
	if got := settings.Load().ModelCompacts; !maps.Equal(got, map[string]int{"deepseek/*": 500000}) {
		t.Errorf("a refused save changed them: %v", got)
	}

	// saved under an older magpie, then the editor saved with it unchanged
	s := settings.Load()
	s.ModelCompacts["deepseek/deepseek-v4-flash"] = 400
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if err := SetModelCompacts("deepseek", map[string]int{"*": 600000, "deepseek-v4-flash": 400}); err != nil {
		t.Errorf("a small size sent back as it was: %v", err)
	}
}
