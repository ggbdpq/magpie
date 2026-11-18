package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"gopkg.in/yaml.v3"
)

// lwj19959632901-alt (#7139): the knowledge base lists a model's reasoning
// ladder (reasoning_options' effort values), and dsh's route offers every
// tier of it, each sent as the level of pi-ai's it is — never the last tier
// alone, which leaves the model's thinking slider nothing to move between.
// The ladders here are the catalog's opencode-go node's, as it listed them
// on 2026-11-19: glm-5.3 low/high/max, glm-5.2 high/max, hy4-preview
// none/high, gemini-4-argon low/medium/high.
func TestDshRouteOffersEveryTierTheCatalogLists(t *testing.T) {
	home, _, web := dshRouteHome(t)
	os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n[]\n"), 0o644)
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	if err := os.WriteFile(catalog.CachePath(), []byte(`{
	  "opencode-go": {"id": "opencode-go", "models": {
	    "glm-5.3": {"id": "glm-5.3", "name": "GLM-5.3", "reasoning": true,
	      "reasoning_options": [{"type": "effort", "values": ["low", "high", "max"]}], "limit": {"context": 1000000}},
	    "glm-5.2": {"id": "glm-5.2", "name": "GLM-5.2", "reasoning": true,
	      "reasoning_options": [{"type": "effort", "values": ["high", "max"]}], "limit": {"context": 1000000}},
	    "hy4-preview": {"id": "hy4-preview", "name": "HY4 Preview", "reasoning": true,
	      "reasoning_options": [{"type": "effort", "values": ["none", "high"]}], "limit": {"context": 1000000}},
	    "gemini-4-argon": {"id": "gemini-4-argon", "name": "Gemini 4 Argon", "reasoning": true,
	      "reasoning_options": [{"type": "effort", "values": ["low", "medium", "high"]}], "limit": {"context": 1000000}}
	  }}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	p := provider.Provider{ID: "ocgo", Name: "OpenCode Go", Key: "k", Chat: "http://127.0.0.1:1/v1", Catalog: "opencode-go",
		Models: []string{"glm-5.3", "glm-5.2", "hy4-preview", "gemini-4-argon"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	a := dsh(home)
	if err := a.Field("model").Set("magpie/ocgo/glm-5.3"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(web)
	if err != nil {
		t.Fatal(err)
	}
	var items []struct {
		ID     string `yaml:"id"`
		Config struct {
			Providers struct {
				Magpie struct {
					Models []struct {
						ID               string            `yaml:"id"`
						ReasoningEfforts map[string]string `yaml:"reasoningEfforts"`
					} `yaml:"models"`
				} `yaml:"magpie"`
			} `yaml:"providers"`
		} `yaml:"config"`
	}
	if err := yaml.Unmarshal(b, &items); err != nil {
		t.Fatal(err)
	}
	models := map[string]map[string]string{}
	for _, it := range items {
		if it.ID != dshPiRow {
			continue
		}
		for _, m := range it.Config.Providers.Magpie.Models {
			models[m.ID] = m.ReasoningEfforts
		}
	}
	if len(models) == 0 {
		t.Fatalf("no magpie route in the profile:\n%s", b)
	}
	// each model's ladder as pi-ai names the tiers: the catalog's none is
	// off, the rest keep their names
	for _, tc := range []struct {
		id    string
		lamps []string
	}{
		{"ocgo/glm-5.3", []string{"low", "high", "max"}},
		{"ocgo/glm-5.2", []string{"high", "max"}},
		{"ocgo/hy4-preview", []string{"off", "high"}},
		{"ocgo/gemini-4-argon", []string{"low", "medium", "high"}},
	} {
		got := models[tc.id]
		if len(got) != len(tc.lamps) {
			t.Errorf("%s offers [%v], want every tier of [%s] (entry written:\n%s)", tc.id, got, strings.Join(tc.lamps, " "), b)
		}
		for _, l := range tc.lamps {
			if got[l] == "" {
				t.Errorf("%s offers no %s tier (entry written:\n%s)", tc.id, l, b)
			}
		}
	}
	if got := models["ocgo/hy4-preview"]["off"]; got != "none" {
		t.Errorf("hy4-preview sends off as %q, want none", got)
	}
	for id, m := range models {
		for level, sent := range m {
			if level == "off" {
				continue // its value is the catalog's none, not off
			}
			if sent != level {
				t.Errorf("%s sends %s as %q, want %s", id, level, sent, level)
			}
		}
	}
}
