package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
)

// TestMoveBiliPrefixKeepsZedPathAndQuery: only the outer /bili/ port
// changes. The path, the query, and a provider that is not bili stay.
func TestMoveBiliPrefixKeepsZedPathAndQuery(t *testing.T) {
	home := syncHome(t)
	cred := ""
	was := zedCredential
	zedCredential = func(url string) error { cred = url; return nil }
	t.Cleanup(func() { zedCredential = was })
	a := zedAt(filepath.Join(home, ".config", "zed"))
	gw := gatewayV1()
	old := "http://127.0.0.1:8787/bili/" + gw + "/extra?q=1"
	next := "http://127.0.0.1:18787/bili/" + gw + "/extra?q=1"
	writeFile(t, a.Path, `{
  // kept
  "language_models": {"openai_compatible": {
    "other": {"api_url": "https://custom.example/v1/mine?keep=1"},
    "magpie": {"api_url": "`+old+`"}
  }}
}
`)
	if err := MoveBiliPrefix(a, "http://127.0.0.1:18787/bili/"); err != nil {
		t.Fatal(err)
	}
	if got := ZedURL(a); got != next {
		t.Fatalf("api_url %q, want %q", got, next)
	}
	if cred != next {
		t.Fatalf("credential saved for %q, want %q", cred, next)
	}
	if got, _ := edit.GetJSON(a.Path, "language_models.openai_compatible.other.api_url"); got != "https://custom.example/v1/mine?keep=1" {
		t.Fatalf("other provider %q", got)
	}
	if !strings.Contains(readFile(a.Path), "// kept") {
		t.Fatalf("comment dropped:\n%s", readFile(a.Path))
	}
	cred = ""
	if err := MoveBiliPrefix(a, "http://127.0.0.1:18787/bili/"); err != nil {
		t.Fatal(err)
	}
	if cred != "" {
		t.Fatalf("an unchanged prefix saved a credential for %q", cred)
	}
}

// TestMoveBiliPrefixScopesHermesAndDshToMagpie: moving the shared daemon's
// prefix touches only magpie's own provider entries in Hermes and dsh.
// User-defined providers pointed at another /bili/ port and YAML comments stay.
func TestMoveBiliPrefixScopesHermesAndDshToMagpie(t *testing.T) {
	home := syncHome(t)
	gw := gatewayV1()
	old := "http://127.0.0.1:8787/bili/"
	next := "http://127.0.0.1:18787/bili/"
	otherBili := "http://127.0.0.1:19555/bili/https://openrouter.ai/api/v1"

	t.Run("hermes", func(t *testing.T) {
		root := filepath.Join(home, ".hermes", "config.yaml")
		bot := filepath.Join(home, ".hermes", "profiles", "bot", "config.yaml")
		writeFile(t, root, "# keep comment http://127.0.0.1:8787/bili/ignored\nproviders:\n  magpie:\n    base_url: "+old+gw+"/extra?q=1\n  magpie-anthropic:\n    base_url: "+old+"http://127.0.0.1:4567/anthropic\n  openrouter:\n    base_url: "+otherBili+"\n")
		writeFile(t, bot, "providers:\n  openrouter:\n    base_url: "+otherBili+"\n")
		a := hermesIn(here(home))
		if err := MoveBiliPrefix(a, next); err != nil {
			t.Fatal(err)
		}
		gotRoot := readFile(root)
		if !strings.Contains(gotRoot, "# keep comment http://127.0.0.1:8787/bili/ignored") {
			t.Fatalf("comment modified:\n%s", gotRoot)
		}
		if v, _ := edit.GetYAML(root, "providers.magpie.base_url"); v != next+gw+"/extra?q=1" {
			t.Fatalf("magpie base_url %q", v)
		}
		if v, _ := edit.GetYAML(root, "providers.magpie-anthropic.base_url"); v != next+"http://127.0.0.1:4567/anthropic" {
			t.Fatalf("magpie-anthropic base_url %q", v)
		}
		if v, _ := edit.GetYAML(root, "providers.openrouter.base_url"); v != otherBili {
			t.Fatalf("non-magpie openrouter base_url clobbered: %q", v)
		}
		if gotBot := readFile(bot); !strings.Contains(gotBot, otherBili) {
			t.Fatalf("profile openrouter clobbered:\n%s", gotBot)
		}
	})

	t.Run("dsh", func(t *testing.T) {
		dir := filepath.Join(home, ".dsh")
		web := filepath.Join(dir, "profiles", "web", "cordis.patch.yml")
		homePatch := filepath.Join(dir, "cordis.patch.yml")
		settingsPath := filepath.Join(dir, "settings.yaml")
		// When profiles exist, a non-list legacy config.yaml must be ignored (matching dshFiles).
		nonList := filepath.Join(dir, "config.yaml")
		writeFile(t, web, "# keep comment http://127.0.0.1:8787/bili/ignored\n- id: llm-pi-ai\n  config:\n    providers:\n      magpie:\n        baseURL: "+old+gw+"/extra?q=1\n      magpie-anthropic:\n        baseURL: "+old+"http://127.0.0.1:4567\n      openrouter:\n        baseURL: "+otherBili+"\n")
		writeFile(t, homePatch, "- id: llm-pi-ai\n  config:\n    providers:\n      openrouter:\n        baseURL: "+otherBili+"\n")
		writeFile(t, settingsPath, "llm-pi-ai:\n  providers:\n    magpie:\n      baseURL: "+old+gw+"\n    openrouter:\n      baseURL: "+otherBili+"\n")
		writeFile(t, nonList, "not_a_list: true\n")
		a := dshIn(here(home))
		if err := MoveBiliPrefix(a, next); err != nil {
			t.Fatal(err)
		}
		gotWeb := readFile(web)
		if !strings.Contains(gotWeb, "# keep comment http://127.0.0.1:8787/bili/ignored") {
			t.Fatalf("web comment modified:\n%s", gotWeb)
		}
		if !strings.Contains(gotWeb, "baseURL: "+next+gw+"/extra?q=1") || !strings.Contains(gotWeb, "baseURL: "+next+"http://127.0.0.1:4567") {
			t.Fatalf("magpie routes not moved:\n%s", gotWeb)
		}
		if !strings.Contains(gotWeb, otherBili) {
			t.Fatalf("web openrouter clobbered:\n%s", gotWeb)
		}
		if gotHome := readFile(homePatch); !strings.Contains(gotHome, otherBili) {
			t.Fatalf("home patch openrouter clobbered:\n%s", gotHome)
		}
		if v, _ := edit.GetYAML(settingsPath, "llm-pi-ai.providers.magpie.baseURL"); v != next+gw {
			t.Fatalf("settings.yaml magpie baseURL %q", v)
		}
		if v, _ := edit.GetYAML(settingsPath, "llm-pi-ai.providers.openrouter.baseURL"); v != otherBili {
			t.Fatalf("settings.yaml openrouter clobbered: %q", v)
		}
		if gotNonList := readFile(nonList); gotNonList != "not_a_list: true\n" {
			t.Fatalf("non-list config.yaml modified while profiles exist:\n%s", gotNonList)
		}
	})

	t.Run("dsh legacy without profiles", func(t *testing.T) {
		legacyHome := t.TempDir()
		dir := filepath.Join(legacyHome, ".dsh")
		legacy := filepath.Join(dir, "config.yaml")
		writeFile(t, legacy, "- id: llm-deepseek # magpie\n  config:\n    baseURL: "+old+gw+"\n")
		a := dshIn(here(legacyHome))
		if err := MoveBiliPrefix(a, next); err != nil {
			t.Fatal(err)
		}
		if gotLegacy := readFile(legacy); !strings.Contains(gotLegacy, next+gw) || !strings.Contains(gotLegacy, "# magpie") {
			t.Fatalf("legacy config.yaml:\n%s", gotLegacy)
		}
	})
}
