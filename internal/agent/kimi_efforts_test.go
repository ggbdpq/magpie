package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// #333: the new Kimi Code is told each thinking model's levels
// (support_efforts, default_effort high where there is one), or its picker
// has thinking on and off only; none is its own off, so it isn't a level,
// and kimi-cli, which knows neither key, gets neither.
func TestKimiEfforts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("KIMI_SHARE_DIR", "")
	t.Setenv("KIMI_CODE_HOME", "")
	if err := provider.Save(provider.Provider{
		ID: "think", Name: "Think", Chat: "https://example.test/v1", Key: "key",
		Models: []string{"deep", "wide", "plain"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("think", "https://example.test/v1", []catalog.Model{
		{ID: "deep", Efforts: []string{"none", "high", "max"}},
		{ID: "wide", Efforts: []string{"low", "medium"}},
		{ID: "plain"},
	}); err != nil {
		t.Fatal(err)
	}
	type model struct {
		Capabilities   []string `toml:"capabilities"`
		SupportEfforts []string `toml:"support_efforts"`
		DefaultEffort  string   `toml:"default_effort"`
	}
	read := func(path string) map[string]model {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var c struct{ Models map[string]model }
		if err := toml.Unmarshal(b, &c); err != nil {
			t.Fatalf("%v\n%s", err, b)
		}
		return c.Models
	}

	path := filepath.Join(home, ".kimi-code", "config.toml")
	writeFile(t, path, "")
	if err := kimi(home).Field("model").Set("magpie/think/deep"); err != nil {
		t.Fatal(err)
	}
	ms := read(path)
	for k, want := range map[string]model{
		// wide has no none among its levels: its thinking can't go off
		// (always_thinking, #3399)
		"magpie/think/deep":  {[]string{"thinking", "tool_use"}, []string{"high", "max"}, "high"},
		"magpie/think/wide":  {[]string{"thinking", "always_thinking", "tool_use"}, []string{"low", "medium"}, ""},
		"magpie/think/plain": {[]string{"tool_use"}, nil, ""},
	} {
		if got := ms[k]; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %+v, want %+v", k, got, want)
		}
	}

	os.RemoveAll(filepath.Join(home, ".kimi-code"))
	legacy := filepath.Join(home, ".kimi", "config.toml")
	writeFile(t, legacy, "")
	if err := kimi(home).Field("model").Set("magpie/think/deep"); err != nil {
		t.Fatal(err)
	}
	if got := read(legacy)["magpie/think/deep"]; got.SupportEfforts != nil || got.DefaultEffort != "" {
		t.Errorf("kimi-cli's: %+v", got)
	}
}

// #3399: a thinking model with no none among its levels — Kimi's k3.1-pro —
// cannot have its thinking turned off: Kimi's own entry for it declares
// always_thinking, and its upstream refuses a session run at off with a
// 400. magpie's table for it says so too; one with none (k3) doesn't, and
// kimi-cli, which has no word for it as for tool_use, gets neither.
func TestKimiAlwaysThinking(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("KIMI_SHARE_DIR", "")
	t.Setenv("KIMI_CODE_HOME", "")
	if err := provider.Save(provider.Provider{
		ID: "moonshot", Name: "Moonshot", Chat: "https://example.test/v1", Key: "key",
		Models: []string{"k3.1-pro", "k3"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("moonshot", "https://example.test/v1", []catalog.Model{
		{ID: "k3.1-pro", Efforts: []string{"low", "high", "max"}},
		{ID: "k3", Efforts: []string{"none", "low", "high", "max"}},
	}); err != nil {
		t.Fatal(err)
	}
	type model struct {
		Capabilities   []string `toml:"capabilities"`
		SupportEfforts []string `toml:"support_efforts"`
		DefaultEffort  string   `toml:"default_effort"`
	}
	read := func(path string) map[string]model {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var c struct {
			Models map[string]model
		}
		if err := toml.Unmarshal(b, &c); err != nil {
			t.Fatalf("%v\n%s", err, b)
		}
		return c.Models
	}

	path := filepath.Join(home, ".kimi-code", "config.toml")
	writeFile(t, path, "")
	if err := kimi(home).Field("model").Set("magpie/moonshot/k3.1-pro"); err != nil {
		t.Fatal(err)
	}
	ms := read(path)
	for k, want := range map[string]model{
		"magpie/moonshot/k3.1-pro": {[]string{"thinking", "always_thinking", "tool_use"}, []string{"low", "high", "max"}, "high"},
		"magpie/moonshot/k3":       {[]string{"thinking", "tool_use"}, []string{"low", "high", "max"}, "high"},
	} {
		if got := ms[k]; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %+v, want %+v", k, got, want)
		}
	}

	os.RemoveAll(filepath.Join(home, ".kimi-code"))
	legacy := filepath.Join(home, ".kimi", "config.toml")
	writeFile(t, legacy, "")
	if err := kimi(home).Field("model").Set("magpie/moonshot/k3.1-pro"); err != nil {
		t.Fatal(err)
	}
	if got := read(legacy)["magpie/moonshot/k3.1-pro"]; !reflect.DeepEqual(got, model{Capabilities: []string{"thinking"}}) {
		t.Errorf("kimi-cli's: %+v", got)
	}
}
