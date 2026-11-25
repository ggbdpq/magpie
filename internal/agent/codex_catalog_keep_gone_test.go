package agent

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// catalogSlugs reads the catalog list as its entries' slugs and
// visibilities, in order.
func catalogSlugs(t *testing.T, list string) (slugs []struct {
	Slug       string `json:"slug"`
	Visibility string `json:"visibility"`
}) {
	t.Helper()
	var got struct {
		Models []struct {
			Slug       string `json:"slug"`
			Visibility string `json:"visibility"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(readFile(list)), &got); err != nil {
		t.Fatalf("%s: %v", list, err)
	}
	return got.Models
}

// A Codex thread keeps the model id it started on and checks it against
// the catalog magpie writes for Codex (magpie-models.json) before it
// sends: with the id gone from the catalog, every send in the thread is
// refused client-side — "The '<provider>/<model>' model is not supported
// when using Codex with a ChatGPT account", codex_error_info
// model_not_found — and no request ever reaches magpie (#4864, sljdxde:
// 44 threads unsendable after a provider's list changed). So an id the
// provider no longer lists keeps an entry, hidden: no picker offers it,
// and a request naming it is routed to the provider as any unlisted
// model's (#2642).
func TestCodexCatalogKeepsAGoneModelForItsThreads(t *testing.T) {
	home, _ := codexHome(t, "", "")
	list := filepath.Join(home, ".codex", "magpie-models.json")
	cx := codex(home)
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	var sawM1 bool
	for _, m := range catalogSlugs(t, list) {
		if m.Slug == "fake/m1" {
			sawM1 = true
		}
	}
	if !sawM1 {
		t.Fatalf("not set to begin with:\n%s", readFile(list))
	}
	// the provider's list changes, and fake/m1 is no longer in it
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m2"}}); err != nil {
		t.Fatal(err)
	}
	SyncCatalog()
	var old, added bool
	for _, m := range catalogSlugs(t, list) {
		switch m.Slug {
		case "fake/m1":
			old = true
			if m.Visibility != "hide" {
				t.Errorf("a model the provider no longer serves is still offered: visibility %q", m.Visibility)
			}
		case "fake/m2":
			added = true
			if m.Visibility != "list" {
				t.Errorf("the provider's pick is not offered: visibility %q", m.Visibility)
			}
		}
	}
	if !added {
		t.Errorf("sync didn't write the provider's new model:\n%s", readFile(list))
	}
	if !old {
		t.Errorf("the entry of a model existing threads name went from magpie-models.json (#4864):\n%s", readFile(list))
	}
}
