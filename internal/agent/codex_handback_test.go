package agent

import (
	"strings"
	"testing"
)

// A reading that says nothing keeps the takeover (#5316, zqbake). The
// hand-back went by the absence of a known hold, so a magpie just started,
// or a vendor that answered nothing, gave Codex back to a ChatGPT account
// still out while its app-server ran on magpie — and every thread of
// either provider then failed the app's provider check (liu95270: "切换到
// codex 出现这个问题 Model provider mismatch", #5480). A reading that says
// room gives it back as before.
func TestCodexTakeoverHoldsThroughReadingsThatSayNothing(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, "")
	cx := codex(home)
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	oldRoom := codexRoomKnown
	t.Cleanup(func() { codexRoomKnown = oldRoom })
	codexUsedUp = func() bool { return true }
	t.Cleanup(func() { codexUsedUp = func() bool { return false } })
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `model_provider = "magpie"`) {
		t.Fatalf("held: magpie not Codex's provider:\n%s", cfg)
	}

	// the reading says nothing any more: the takeover keeps, and the
	// config stays where the running app-server reads it as magpie
	codexUsedUp = func() bool { return false }
	codexRoomKnown = func() bool { return false }
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `model_provider = "magpie"`) {
		t.Fatalf("a reading that said nothing handed Codex back, its app-server still on magpie:\n%s", cfg)
	}

	// room, plainly said: back beside the sign-in as before
	codexRoomKnown = func() bool { return true }
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "model_provider =") || strings.Contains(cfg, "model_catalog_json") ||
		!strings.Contains(cfg, "openai_base_url") || !strings.Contains(cfg, `model = "fake/m1"`) {
		t.Fatalf("room: not back beside the sign-in:\n%s", cfg)
	}
}
