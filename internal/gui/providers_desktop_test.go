package gui

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// The number Claude Desktop brackets a model's menu row with (#2578) is
// said on the Providers page too, on the model's own row, so a menu row
// there can be matched back to its provider: the number the page says is
// the one in the id Claude Desktop is shown (gateway.DesktopID).
func TestProviderModelsSayDesktopNumber(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p := provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"claude-opus-5-5", "m2", "sonnet-5-1m"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelEfforts("relay/claude-opus-5-5", []string{"low", "high"}); err != nil {
		t.Fatal(err)
	}
	var num = regexp.MustCompile(`magpie-(\d+)`)
	// the number in the id Claude Desktop is shown, "" when the id there
	// is the model's own
	want := func(id string) string {
		desktop := ""
		for _, e := range provider.Catalog() {
			if e.ID == id {
				if m := num.FindStringSubmatch(gateway.DesktopID(e)); m != nil {
					desktop = m[1]
				}
			}
		}
		return desktop
	}
	raw, err := json.Marshal(providerInfo(p, nil))
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	said := map[string]any{}
	for _, m := range page.Models {
		said[m["id"].(string)] = m["desktop"]
	}
	for _, c := range []struct{ id, form string }{
		// an effort Claude model: magpie-<n>.anthropic.claude-opus-5-5,
		// the shape the issue's menu row folds into a catalog name
		{"relay/claude-opus-5-5", "magpie-<n>.anthropic.<model>"},
		// no efforts and no Claude word in it: anthropic/magpie-<n>
		{"relay/m2", "anthropic/magpie-<n>"},
		// a Claude word but no level and no Claude model's shape: its own
		// id, which carries no number
		{"relay/sonnet-5-1m", "own"},
	} {
		w := want(c.id)
		if c.form == "own" && w != "" {
			t.Fatalf("%s: Desktop is shown %q, want its own id", c.id, w)
		}
		if got := said[c.id[len("relay/"):]]; got != w && !(w == "" && got == nil) {
			t.Errorf("%s: the page says desktop %v, want %q (%s)", c.id, got, w, c.form)
		}
	}
}
