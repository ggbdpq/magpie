package agent

import (
	"strings"
	"testing"
)

// With account discovery off (#1535) magpie reads no auth.json, so picking
// the ChatGPT sign-in in Codex's login field is the only word on whether
// Codex is signed in: the pick is the user saying it is, and set() is to
// wire by it as it does with the read answering. It wired by nothing, and
// config.toml kept magpie as Codex's provider while the pick read back as
// saved (#6917). The wiring the read lets set() make is what the pick
// makes: magpie beside the sign-in, its model on, its provider gone.
func TestCodexLoginPickWithDiscoveryOff(t *testing.T) {
	for _, c := range []struct{ name, pick string }{
		{"ChatGPT", ""},
		{"AlwaysChatGPT", "chatgpt"},
	} {
		t.Run(c.name, func(t *testing.T) {
			home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, `model = "gpt-5.5"`+"\n")
			t.Setenv("MAGPIE_NO_ACCOUNTS", "1")
			cx := codex(home)
			// from magpie as the provider, on one of magpie's models: the
			// state the switch starts from
			if err := cx.Fields[0].Set("fake/m1"); err != nil {
				t.Fatal(err)
			}
			if cfg := read(); !strings.Contains(cfg, `model_provider = "magpie"`) || !strings.Contains(cfg, `model = "fake/m1"`) {
				t.Fatalf("not magpie's provider to start:\n%s", cfg)
			}
			if err := cx.Field("login").Set(c.pick); err != nil {
				t.Fatal(err)
			}
			cfg := read()
			if strings.Contains(cfg, "model_provider =") || strings.Contains(cfg, "model_catalog_json") ||
				!strings.Contains(cfg, `model = "fake/m1"`) || !strings.Contains(cfg, `openai_base_url = "http://127.0.0.1:`) {
				t.Fatalf("sign-in picked %q:\n%s", c.pick, cfg)
			}
			if !cx.Beside() {
				t.Fatalf("sign-in picked %q: not beside the sign-in:\n%s", c.pick, cfg)
			}
		})
	}
}
