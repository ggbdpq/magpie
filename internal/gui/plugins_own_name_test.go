package gui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// #4558 (MYCF711): a git-installed plugin that says what it is in its own
// package.json (magpie.name) was shown by its dependency key in
// Settings → Plugins — nothing read the field. The state the page is told
// carries the plugin's own name, none when it gives none, so the row can
// show it where the market names no entry of the package.
func TestPluginsStateGivesThePluginsOwnName(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("MAGPIE_BUN", "")
	t.Setenv("PATH", "")
	dir := func(name, own string) string {
		d := filepath.Join(h, "src", name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		pj := map[string]any{"name": name, "version": "1.0.0", "main": "index.js"}
		if own != "" {
			pj["magpie"] = map[string]any{"name": own}
		}
		b, _ := json.Marshal(pj)
		if err := os.WriteFile(filepath.Join(d, "package.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
		return d
	}
	named, plain := dir("magpie-officeace", "华为云 HUAWEI"), dir("magpie-magicrew-auth", "")
	if err := os.MkdirAll(settings.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"plugins": []map[string]any{{"spec": named}, {"spec": plain}}})
	if err := os.WriteFile(filepath.Join(settings.Dir(), "plugins.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	// read as the page does, through the JSON: the row's name is new, and
	// the Go struct would compile the want away
	st, err := json.Marshal(pluginsState(context.Background(), nil))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Plugins []struct {
			Spec string `json:"spec"`
			Name string `json:"name"`
		} `json:"plugins"`
	}
	if json.Unmarshal(st, &got) != nil {
		t.Fatal(err)
	}
	if len(got.Plugins) != 2 {
		t.Fatalf("plugins: %v", got.Plugins)
	}
	bySpec := map[string]string{}
	for _, p := range got.Plugins {
		bySpec[p.Spec] = p.Name
	}
	if bySpec[named] != "华为云 HUAWEI" {
		t.Errorf("the plugin's own name: %q, want 华为云 HUAWEI", bySpec[named])
	}
	if bySpec[plain] != "" {
		t.Errorf("none given: %q, want none", bySpec[plain])
	}
}
