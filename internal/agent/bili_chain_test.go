package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// 33sczf on X: the Library's billion-context tab put Claude Code behind
// bili's lane, ANTHROPIC_BASE_URL
// http://127.0.0.1:18787/bili/http://127.0.0.1:3425, which forwards to the
// gateway. The Agents page called it "配置已变更", and Reconnect would
// have written the gateway over the user's billion-context. The lane runs
// only beside its agent, so nothing answers GET / there as magpie: every
// agent whose address names the gateway at the end of a local proxy's
// path still reaches magpie, with no probe, and a sync keeps the chain.
func TestBiliChainIsStillMagpie(t *testing.T) {
	t.Setenv("MAGPIE_ADDR", "")
	home, _ := codexHome(t, "", "")
	managed := claudeManaged
	claudeManaged = func() string { return filepath.Join(home, "managed-settings.json") }
	t.Cleanup(func() { claudeManaged = managed })
	os.MkdirAll(filepath.Join(home, ".hanako", "agents", "hana"), 0o755)
	os.WriteFile(filepath.Join(home, ".hanako", "agents", "hana", "config.yaml"), []byte("agent:\n  name: Hana\n"), 0o644)
	craftFixture(t, home)
	t.Setenv("ASTRBOT_ROOT", "")
	writeFile(t, filepath.Join(home, ".astrbot", "data", "cmd_config.json"), astrbotConfig)
	qwenpawFixture(t, home)
	setPort(t, 3592)
	const gw = "http://127.0.0.1:3592"
	const lane = "http://127.0.0.1:18787/bili/"
	var agents []*Agent
	for _, a := range All() {
		if a.Check == nil || a.Launch != nil || a.WSL != "" || a.Native != nil || a.ID == "alma" || a.ID == "shelley" || a.ID == "cursorlocal" {
			continue
		}
		var key, want string
		for _, g := range a.Fields {
			for _, o := range g.Options(a.Values()) {
				if want == "" && (o.Ref == "fake/m1" || o.Value == magpieID) {
					key, want = g.Key, o.Value
				}
			}
		}
		if want == "" {
			t.Fatalf("%s: no field takes magpie's models", a.ID)
		}
		if err := a.Apply(key, want); err != nil {
			t.Fatalf("%s: %v", a.ID, err)
		}
		agents = append(agents, a)
	}
	moved := filesWith(home, "127.0.0.1:3592")
	if len(moved) < 20 {
		t.Fatalf("only %d files have the gateway's address: %v", len(moved), moved)
	}
	for _, p := range moved {
		b, _ := os.ReadFile(p)
		os.WriteFile(p, []byte(strings.ReplaceAll(string(b), gw, lane+gw)), 0o644)
	}

	// the lane is down, or answers GET / as itself: never asked
	asked := fakeMagpieAt(t, map[string]bool{})
	for _, a := range agents {
		if a.ID == "codex" {
			// its openai_base_url is held to the gateway itself; bili's
			// Codex lane is an MCP server, never this address
			continue
		}
		if c := a.Check(); c != "" {
			t.Errorf("%s behind billion-context: %s", a.ID, c)
		}
		if d := a.Drift(); d != nil && d.Kind == "unwired" {
			t.Errorf("%s behind billion-context drifted: %+v", a.ID, d)
		}
	}
	for _, b := range asked() {
		if strings.Contains(b, "/bili/") {
			t.Errorf("asked %s whether it is magpie", b)
		}
	}

	// a sync, which a pick and Reconnect write through, keeps the chain
	if err := provider.Save(provider.Provider{ID: "fake2", Name: "Fake2", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m2"}}); err != nil {
		t.Fatal(err)
	}
	SyncCatalog()
	for _, p := range moved {
		b, _ := os.ReadFile(p)
		s, name := string(b), strings.TrimPrefix(p, home)
		if strings.Count(s, gw) != strings.Count(s, lane+gw) {
			t.Errorf("%s is back on the gateway past billion-context:\n%s", name, s)
		}
	}
}

func TestChainsToGateway(t *testing.T) {
	for v, want := range map[string]bool{
		"http://127.0.0.1:18787/bili/http://127.0.0.1:3425":          true,
		"http://127.0.0.1:18787/bili/http://127.0.0.1:3425/":         true,
		"http://localhost:8787/bili/anthropic/http://127.0.0.1:3425": true,
		"http://127.0.0.1:8787/bili/http://localhost:3425":           true,
		"http://127.0.0.1:18787/bili/http://127.0.0.1:3426":          false,
		"http://127.0.0.1:18787/bili/https://api.anthropic.com":      false,
		"http://127.0.0.1:18787/bili/http://127.0.0.1:3425/v1":       false,
		"https://relay.example/bili/http://127.0.0.1:3425":           false,
		"http://127.0.0.1:3425":                                      false,
		"http://127.0.0.1:18787/bili/http://user@127.0.0.1:3425":     false,
		"http://127.0.0.1:18787/bili/http://evil.example:3425":       false,
	} {
		if got := chainsToGateway(v, "http://127.0.0.1:3425"); got != want {
			t.Errorf("chainsToGateway(%s) = %v, want %v", v, got, want)
		}
	}
	if !chainsToGateway("http://127.0.0.1:8787/bili/http://127.0.0.1:3425/v1", "http://127.0.0.1:3425/v1") {
		t.Error("an OpenAI agent's /v1 through bili isn't magpie")
	}
}

// The reporter's own: Claude Code behind bili's lane, which isn't running.
// No drift, and a pick (what Reconnect and the model picker write) keeps
// ANTHROPIC_BASE_URL on the lane.
func TestClaudeBehindBiliLaneKept(t *testing.T) {
	home, path := claudeSettings(t, `{}`)
	saveDeepseek(t)
	cl := claude(home)
	if err := cl.Apply("model", "deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	gw, _ := edit.GetJSON(path, "env.ANTHROPIC_BASE_URL")
	lane := "http://127.0.0.1:18787/bili/" + gw
	if err := edit.SetJSON(path, edit.KV{Path: "env.ANTHROPIC_BASE_URL", Value: lane}); err != nil {
		t.Fatal(err)
	}
	fakeMagpieAt(t, map[string]bool{})
	if c := cl.Check(); c != "" {
		t.Fatalf("Check: %s", c)
	}
	if d := cl.Drift(); d != nil {
		t.Fatalf("drift: %+v", d)
	}
	if err := cl.Apply("model", "deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if u, _ := edit.GetJSON(path, "env.ANTHROPIC_BASE_URL"); u != lane {
		t.Fatalf("a pick wrote ANTHROPIC_BASE_URL = %s over the lane %s", u, lane)
	}
}

func TestBiliChainPrefix(t *testing.T) {
	for v, want := range map[string]string{
		"http://127.0.0.1:18787/bili/https://api.anthropic.com":                             "http://127.0.0.1:18787/bili/",
		"http://127.0.0.1:18787/bili/https://api.anthropic.com/":                            "http://127.0.0.1:18787/bili/",
		"http://localhost:8787/bili/anthropic/https://api.anthropic.com":                    "http://localhost:8787/bili/anthropic/",
		"http://[::1]:18787/bili/https://api.anthropic.com":                                 "http://[::1]:18787/bili/",
		"http://127.0.0.1:18787/bili/http://127.0.0.1:3425":                                 "http://127.0.0.1:18787/bili/",
		"http://127.0.0.1:18787/bili/http://127.0.0.1:18787/bili/https://api.anthropic.com": "http://127.0.0.1:18787/bili/",
		"http://127.0.0.1:18787/other/https://api.anthropic.com":                            "",
		"https://relay.example/bili/https://api.anthropic.com":                              "",
		"http://127.0.0.1:18787/bili/":                                                      "",
		"http://127.0.0.1:3425":                                                             "",
		"http://127.0.0.1:18787/bili/http://user@127.0.0.1:3425":                            "",
	} {
		if got := biliChainPrefix(v); got != want {
			t.Errorf("biliChainPrefix(%q) = %q, want %q", v, got, want)
		}
	}
}

// #7846: enabling billion-context for Claude Code before connecting it in
// magpie leaves ANTHROPIC_BASE_URL on http://127.0.0.1:18787/bili/<upstream>.
// Connecting Claude Code, syncing, reconnecting after a gateway port change,
// and disconnecting/reconnecting must all keep the /bili/ prefix in front of
// magpie's gateway without stacking it.
func TestClaudeConnectKeepsBiliLane(t *testing.T) {
	t.Setenv("MAGPIE_ADDR", "")
	const prefix = "http://127.0.0.1:18787/bili/"
	const orig = prefix + "https://api.anthropic.com"
	home, path := claudeSettings(t, `{"env":{"ANTHROPIC_BASE_URL":"`+orig+`"}}`)
	saveDeepseek(t)
	setPort(t, 3592)
	fakeMagpieAt(t, map[string]bool{})

	cl := claude(home)
	if err := cl.Apply("model", "deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	want := prefix + "http://127.0.0.1:3592"
	if got, _ := edit.GetJSON(path, "env.ANTHROPIC_BASE_URL"); got != want {
		t.Fatalf("after connect: ANTHROPIC_BASE_URL = %q, want %q", got, want)
	}
	if c := cl.Check(); c != "" {
		t.Fatalf("Check after connect: %s", c)
	}
	if d := cl.Drift(); d != nil {
		t.Fatalf("Drift after connect: %+v", d)
	}

	// another pick and SyncCatalog keep the same single /bili/ prefix
	if err := cl.Apply("model", "deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	SyncCatalog()
	if got, _ := edit.GetJSON(path, "env.ANTHROPIC_BASE_URL"); got != want {
		t.Fatalf("after pick+sync: ANTHROPIC_BASE_URL = %q, want %q", got, want)
	}

	// gateway port change -> Drift reports unwired, Reapply updates the upstream port while keeping /bili/
	setPort(t, 3599)
	if d := cl.Drift(); d == nil || d.Kind != "unwired" {
		t.Fatalf("Drift after port change = %+v, want unwired", d)
	}
	if err := cl.Reapply(); err != nil {
		t.Fatal(err)
	}
	wantNew := prefix + "http://127.0.0.1:3599"
	if got, _ := edit.GetJSON(path, "env.ANTHROPIC_BASE_URL"); got != wantNew {
		t.Fatalf("after port change Reapply: ANTHROPIC_BASE_URL = %q, want %q", got, wantNew)
	}
	if c := cl.Check(); c != "" {
		t.Fatalf("Check after Reapply: %s", c)
	}
	if d := cl.Drift(); d != nil {
		t.Fatalf("Drift after Reapply: %+v", d)
	}

	// Disconnect restores the original bili -> Anthropic URL; Connect re-chains bili -> magpie
	if err := cl.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if got, _ := edit.GetJSON(path, "env.ANTHROPIC_BASE_URL"); got != orig {
		t.Fatalf("after Disconnect: ANTHROPIC_BASE_URL = %q, want %q", got, orig)
	}
	if err := cl.Connect(); err != nil {
		t.Fatal(err)
	}
	if got, _ := edit.GetJSON(path, "env.ANTHROPIC_BASE_URL"); got != wantNew {
		t.Fatalf("after Connect: ANTHROPIC_BASE_URL = %q, want %q", got, wantNew)
	}
}
