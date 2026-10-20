package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// A user who keeps magpie's key out of opencode.json writes
// options.apiKey as a reference to a variable, which OpenCode expands from
// its environment as it loads the config ({env:VAR},
// packages/core/src/config/variable.ts). A reference names the key
// OpenCode sends: one that reaches magpie is not drift and a sync keeps
// it; one magpie can't see is unknown, kept and not drift; one naming a
// key the gateway doesn't take is drift, and a sync puts magpie's key in
// its place, a variable of the shell's being nowhere magpie can write it
// (#3040).
func TestOpenCodeWithAKeyInTheEnvironment(t *testing.T) {
	home := syncHome(t)
	t.Setenv("MAGPIE_API_KEY", "")
	os.Unsetenv("MAGPIE_API_KEY")
	cfg := filepath.Join(home, ".config", "opencode", "opencode.json")
	writeFile(t, cfg, `{"model":"magpie/relay/glm-4.6","provider":{"magpie":{"npm":"@ai-sdk/openai-compatible","name":"magpie","options":{"baseURL":"`+gatewayV1()+`","apiKey":"{env:MAGPIE_API_KEY}"},"models":{"relay/glm-4.6":{"name":"GLM-4.6"}}}}}`)
	oc := opencode(home, filepath.Join(home, ".config"))
	secret, err := access.Update("add-key", access.Change{Name: "opencode"})
	if err != nil {
		t.Fatal(err)
	}
	ref := func(what string) {
		t.Helper()
		if v, _ := edit.GetJSON(cfg, "provider.magpie.options.apiKey"); v != "{env:MAGPIE_API_KEY}" {
			t.Fatalf("%s: the user's reference was written over: %q", what, v)
		}
	}
	for _, c := range []struct{ name, key string }{
		{"a gateway key", secret},
		{"magpie's own key", gateway.Token},
	} {
		t.Setenv("MAGPIE_API_KEY", c.key)
		if d := oc.Check(); d != "" {
			t.Fatalf("%s in the environment is drift: %s", c.name, d)
		}
		if err := oc.Sync(); err != nil {
			t.Fatal(err)
		}
		ref(c.name)
		if err := oc.Fields[0].Set("magpie/relay/glm-4.6"); err != nil {
			t.Fatal(err)
		}
		ref(c.name + ", then a pick")
	}
	// a variable only the shell sets, which magpie doesn't see: unknown
	os.Unsetenv("MAGPIE_API_KEY")
	if d := oc.Check(); d != "" {
		t.Fatalf("a variable magpie can't see is drift: %s", d)
	}
	if err := oc.Sync(); err != nil {
		t.Fatal(err)
	}
	ref("a variable magpie can't see")
	// a key the gateway doesn't take
	t.Setenv("MAGPIE_API_KEY", "sk-not-this-gateways")
	if d := oc.Check(); !strings.Contains(d, "apiKey") {
		t.Fatalf("a key the gateway doesn't take is not reported: %q", d)
	}
	if err := oc.Sync(); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetJSON(cfg, "provider.magpie.options.apiKey"); v != gateway.Token {
		t.Fatalf("a sync left %q in place of the reference naming a key the gateway doesn't take", v)
	}
	if d := oc.Check(); d != "" {
		t.Fatalf("after a sync: %s", d)
	}
	// more than the one reference: magpie's key, as before
	if err := edit.SetJSON(cfg, edit.KV{Path: "provider.magpie.options.apiKey", Value: "x{env:MAGPIE_API_KEY}"}); err != nil {
		t.Fatal(err)
	}
	if err := oc.Sync(); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetJSON(cfg, "provider.magpie.options.apiKey"); v != gateway.Token {
		t.Fatalf("a value that doesn't reach magpie is kept: %q", v)
	}
}
