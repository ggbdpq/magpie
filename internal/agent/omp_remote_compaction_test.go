package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
	"gopkg.in/yaml.v3"
)

// omp's remote compaction (#5065): omp 18.15 asks a provider for its own
// compaction (POST {baseUrl}/responses, a compaction_trigger) only with
// remoteCompaction enabled, and on loopback with v2StreamingEnabled too.
// Where every model magpie's write lists as openai-responses compacts
// natively (the provider's Compaction row on, a Responses wire, the model
// asked on Responses there), magpie writes the block, so omp compacts on
// the provider instead of taking magpie's summary for a successful one and
// never falling back. A block the user wrote stays as it is, enabled: false
// included; one listed Responses model that can't compact natively — and no
// block is written, and none that is there is taken away.
func TestOmpRemoteCompaction(t *testing.T) {
	home := syncHome(t)
	was := ompVersion
	ompVersion = func() string { return "18.15.0" }
	t.Cleanup(func() { ompVersion = was })
	dir := filepath.Join(home, ".omp", "agent")
	modelsPath := filepath.Join(dir, "models.yml")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "config.yml"), []byte("modelRoles:\n  default: anthropic/claude-opus-5\n"), 0o644)
	if err := provider.Save(provider.Provider{ID: "resp", Name: "Resp", Key: "k", Responses: "http://127.0.0.1:1/v1", Models: []string{"grok-5", "grok-6"}}); err != nil {
		t.Fatal(err)
	}
	a := omp(home)
	f := a.Field("model")
	block := func() map[string]any {
		var m struct {
			Providers map[string]map[string]any `yaml:"providers"`
		}
		b, err := os.ReadFile(modelsPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := yaml.Unmarshal(b, &m); err != nil {
			t.Fatalf("%v\n%s", err, b)
		}
		mp, _ := m.Providers[magpieID]
		if mp == nil {
			return nil
		}
		rc, _ := mp["remoteCompaction"].(map[string]any)
		return rc
	}

	// native compaction off: omp is sent no triggers it can't have answered
	if err := f.Set("magpie/resp/grok-5"); err != nil {
		t.Fatal(err)
	}
	if rc := block(); rc != nil {
		t.Fatalf("native compaction off, a block anyway: %v", rc)
	}

	// on: the block, its two keys alone — no api of its own, which would
	// send the models asked on other APIs down the Responses compact path
	if err := provider.SetNativeCompaction("resp", true); err != nil {
		t.Fatal(err)
	}
	if err := f.Set("magpie/resp/grok-6"); err != nil {
		t.Fatal(err)
	}
	rc := block()
	if rc == nil || rc["enabled"] != true || rc["v2StreamingEnabled"] != true || len(rc) != 2 {
		t.Fatalf("native compaction on: %v", rc)
	}

	// the user's own block stays as it is, enabled: false included
	if err := edit.SetYAML(modelsPath, edit.KV{Path: "providers.magpie.remoteCompaction", Value: map[string]any{"enabled": false}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if rc = block(); rc == nil || rc["enabled"] != false || rc["v2StreamingEnabled"] != nil {
		t.Fatalf("the user's block rewritten:\n%s", rc)
	}

	// a listed Responses model that can't compact natively: none written.
	// Here a group the gateway serves on Responses (every member best
	// there), one of whose members is set to compact natively no more —
	// omp would send it triggers were the block written
	if err := provider.SetNativeCompaction("resp", false); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "resp2", Name: "Resp2", Key: "k", Responses: "http://127.0.0.1:1/v1", Models: []string{"grok-5"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "pair", Name: "Pair", Members: []string{"resp/grok-5", "resp2/grok-5"}}); err != nil {
		t.Fatal(err)
	}
	edit.DelYAML(modelsPath, "providers.magpie.remoteCompaction")
	if err := f.Set("magpie/group/pair"); err != nil {
		t.Fatal(err)
	}
	if rc := block(); rc != nil {
		t.Fatalf("a member of the group can't compact natively, a block anyway: %v", rc)
	}

	// both members on: the block is written for the group as for a model
	if err := provider.SetNativeCompaction("resp2", true); err != nil {
		t.Fatal(err)
	}
	if rc := block(); rc != nil {
		t.Fatalf("the other member on its own wrote a block: %v", rc)
	}
	if err := provider.SetNativeCompaction("resp", true); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if rc = block(); rc == nil || rc["enabled"] != true || rc["v2StreamingEnabled"] != true || len(rc) != 2 {
		t.Fatalf("the group's members both compact natively: %v", rc)
	}
}
