package agent

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The catalog file magpie writes for a Codex it is the provider of offers
// the service tiers the gateway's dynamic list offers too (#1530): a
// provider the user added by its address is sent the tier Codex asks for
// as it is (#1234), and Codex's /fast exists only on a model whose entry
// lists service_tiers. The file was written with every model's
// service_tiers empty, so a standalone Codex CLI had no /fast however its
// relay served the tier.
func TestCodexCatalogOffersTheTiersTheDynamicListDoes(t *testing.T) {
	home, _ := codexHome(t, "", "")
	list := filepath.Join(home, ".codex", "magpie-models.json")
	if err := provider.Save(provider.Provider{ID: "added", Name: "Added", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"gpt-6.1-sol", "glm-5"}}); err != nil {
		t.Fatal(err)
	}
	cx := codex(home)
	if err := cx.Fields[0].Set("added/gpt-6.1-sol"); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Models []struct {
			Slug  string `json:"slug"`
			Tiers []struct {
				ID string `json:"id"`
			} `json:"service_tiers"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(readFile(list)), &got); err != nil {
		t.Fatal(err)
	}
	tiers := map[string]string{}
	for _, m := range got.Models {
		s := ""
		for _, x := range m.Tiers {
			s += x.ID + " "
		}
		tiers[m.Slug] = s
	}
	if s := tiers["added/gpt-6.1-sol"]; !strings.Contains(s, "priority") {
		t.Errorf("added/gpt-6.1-sol offered %q, want priority (Fast)", s)
	}
	if s := tiers["added/glm-5"]; s != "" {
		t.Errorf("added/glm-5 offered %q, want none", s)
	}
}
