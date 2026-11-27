package codexcat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// A vendor's own list shaped as a Codex catalog entry keeps none of the
// tool-search mark (supports_search_tool), and Codex read its absence as
// no: with the code-mode tool search on, every tool's description went
// with each request — 181,790 tokens a turn for a "你好" — where a mark
// of yes puts the search tool in and leaves the descriptions out
// (#7904). What a row says stands; what it doesn't say is yes, as the
// entries magpie makes itself are.
func TestCatalogSearchToolSaidWhereARowKeepsNone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(os.Getenv("HOME"), ".config"))
	var got struct {
		Models []map[string]any `json:"models"`
	}
	json.Unmarshal(Catalog([]catalog.Model{
		{ID: "relay/gpt-6.2-astra", Name: "GPT-6.2 Astra", Efforts: []string{"low"}, Codex: json.RawMessage(
			`{"slug":"relay/gpt-6.2-astra","description":"Latest frontier agentic coding model.","base_instructions":"You are Codex","shell_type":"shell_command","supported_reasoning_levels":[{"effort":"low","description":"Fast"}]}`)},
		{ID: "relay/gpt-6.2-sol", Name: "GPT-6.2 Sol", Efforts: []string{"low"}, Codex: json.RawMessage(
			`{"slug":"relay/gpt-6.2-sol","description":"Another frontier model.","base_instructions":"You are Codex","shell_type":"shell_command","supports_search_tool":false,"supported_reasoning_levels":[{"effort":"low"}]}`)},
	}), &got)
	by := map[string]map[string]any{}
	for _, e := range got.Models {
		slug, _ := e["slug"].(string)
		by[slug] = e
	}
	if v := by["relay/gpt-6.2-astra"]["supports_search_tool"]; v != true {
		t.Errorf("a row that keeps no mark read as %v, want true\n%v", v, by["relay/gpt-6.2-astra"])
	}
	if v := by["relay/gpt-6.2-sol"]["supports_search_tool"]; v != false {
		t.Errorf("a row that says no was turned to %v", v)
	}
}
