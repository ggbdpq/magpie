package agent

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// #2373: picking a model of a provider added after Pi was wired rewrites
// models.json's magpie provider with the catalog as it is now, so Pi's own
// picker lists the model settings.json's defaultModel names.
func TestPiPickRewritesModelsJSON(t *testing.T) {
	home := syncHome(t)
	a := pi(home)
	if err := a.Field("model").Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	models := filepath.Join(home, ".pi", "agent", "models.json")
	if got := readFile(models); !slices.Contains(modelIDs(t, got), "relay/glm-4.6") {
		t.Fatalf("no magpie models after wiring: %s", got)
	}
	// a new channel signs in, its models join the catalog
	if err := provider.Save(provider.Provider{ID: "trae", Name: "Trae", Key: "k", Chat: "http://127.0.0.1:1/v1",
		Models: []string{"kimi-k3-max"}}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(modelIDs(t, readFile(models)), "trae/kimi-k3-max") {
		t.Fatal("models.json already has the new model before the pick; the test proves nothing")
	}
	// the pick of the new model from the picker
	if err := a.Field("model").Set("magpie/trae/kimi-k3-max"); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(home, ".pi", "agent", "settings.json")
	if m, _ := edit.GetJSON(settings, "defaultModel"); m != "trae/kimi-k3-max" {
		t.Fatalf("settings.json:\n%s", readFile(settings))
	}
	if !slices.Contains(modelIDs(t, readFile(models)), "trae/kimi-k3-max") {
		t.Fatalf("models.json was not rewritten with the picked model:\n%s", readFile(models))
	}
}

// modelIDs are the ids of magpie's models in a Pi models.json.
func modelIDs(t *testing.T, raw string) []string {
	t.Helper()
	var file struct {
		Providers map[string]struct {
			Models []struct {
				ID string `json:"id"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal([]byte(raw), &file); err != nil {
		t.Fatalf("models.json: %v\n%s", err, raw)
	}
	var ids []string
	for _, m := range file.Providers["magpie"].Models {
		ids = append(ids, m.ID)
	}
	return ids
}
