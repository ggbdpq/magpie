package codexcat

import (
	"encoding/json"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// Codex's cache is the list of the account it last fetched with: a Plus or
// Pro Lite one's has no Ultrafast. A ChatGPT account's model another
// account offers Ultrafast on (catalog.Model.Tiers, set by
// provider.CodexTiers) is offered it all the same, in the entry copied from
// the cache and in one magpie writes itself; a group isn't.
func TestUltrafastOfferedPastTheSignedInAccountsCache(t *testing.T) {
	v1Home(t, `{"etag":"W/\"a\"","models":[{"slug":"gpt-6.1-sol","base_instructions":"x","service_tiers":[{"id":"priority","name":"Fast","description":"2x speed, increased usage"}]}]}`)
	ms := []catalog.Model{
		{ID: "codex/gpt-6.1-sol", Name: "a", Tiers: []string{"priority", "ultrafast"}},
		{ID: "codex/gpt-6.2", Name: "b", Tiers: []string{"priority", "ultrafast"}},
		{ID: "codex/gpt-5.5", Name: "c", Tiers: []string{"priority"}},
		{ID: "group/r", Name: "d", Fast: true},
	}
	var got struct {
		Models []struct {
			Slug  string `json:"slug"`
			Tiers []struct {
				ID          string `json:"id"`
				Description string `json:"description"`
			} `json:"service_tiers"`
		} `json:"models"`
	}
	if err := json.Unmarshal(Catalog(ms), &got); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, m := range got.Models {
		s := ""
		for _, x := range m.Tiers {
			s += x.ID + " "
			if x.ID == "ultrafast" && x.Description == "" {
				t.Errorf("%s: Ultrafast without Codex's description", m.Slug)
			}
		}
		ids[m.Slug] = s
	}
	want := map[string]string{"codex/gpt-6.1-sol": "priority ultrafast ", "codex/gpt-6.2": "priority ultrafast ",
		"codex/gpt-5.5": "priority ", "group/r": "priority "}
	for k, v := range want {
		if ids[k] != v {
			t.Errorf("%s offered %q, want %q", k, ids[k], v)
		}
	}
}
