package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/codexcat"
	"github.com/yetone/magpie/internal/settings"
)

func TestCodexCachedEffortUpdatesReachGroups(t *testing.T) {
	effortHome(t)
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "models_cache.json"), []byte(`{"models":[
		{"slug":"m","supports_reasoning_effort_updates":true}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	upstream := &Provider{ID: "a", Account: &Account{Agent: "codex", models: catalog.Codex}}
	models := upstream.Available()
	if len(models) != 1 {
		t.Fatalf("cache listed %d models, want 1", len(models))
	}
	if err := SaveGroup(Group{Name: "cached", Members: []string{"a/m"}}); err != nil {
		t.Fatal(err)
	}
	entries := groupEntries([]Entry{sharedEntry(upstream, models[0], settings.Settings{})})
	listed := codexListed(entries, func(string) []Member { return nil }, true)
	var result struct {
		Models []struct {
			Updates bool `json:"supports_reasoning_effort_updates"`
		} `json:"models"`
	}
	if err := json.Unmarshal(codexcat.Catalog(listed), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Models) != 1 || !result.Models[0].Updates {
		t.Fatalf("cached upstream capability lost on the group's Codex entry: %+v", result.Models)
	}
}

func TestCodexGroupEffortUpdatesFollowEveryMember(t *testing.T) {
	effortHome(t)
	models := parseCodexModels([]byte(`{"models":[
		{"slug":"m","supports_reasoning_effort_updates":true},
		{"slug":"n","supports_reasoning_effort_updates":false},
		{"slug":"unknown"}]}`))
	upstream := &Provider{ID: "a"}
	entries := make([]Entry, 0, len(models))
	for _, model := range models {
		entries = append(entries, sharedEntry(upstream, model, settings.Settings{}))
	}
	for _, group := range []Group{
		{Name: "supported", Members: []string{"a/m"}},
		{Name: "mixed", Members: []string{"a/m", "a/n"}},
		{Name: "unknown", Members: []string{"a/m", "a/unknown"}},
		{Name: "nested", Members: []string{"group/supported"}},
	} {
		if err := SaveGroup(group); err != nil {
			t.Fatal(err)
		}
	}
	groups := groupEntries(entries)
	listed := codexListed(groups, func(string) []Member { return nil }, true)
	encoded := codexcat.Catalog(listed)
	var result struct {
		Models []struct {
			Slug    string `json:"slug"`
			Updates bool   `json:"supports_reasoning_effort_updates"`
		} `json:"models"`
	}
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"group/supported": true,
		"group/mixed":     false,
		"group/unknown":   false,
		"group/nested":    true,
	}
	for _, model := range result.Models {
		expected, exists := want[model.Slug]
		if !exists || model.Updates != expected {
			t.Errorf("%s: effort updates = %v, want %v", model.Slug, model.Updates, expected)
		}
		delete(want, model.Slug)
	}
	if len(want) != 0 {
		t.Fatalf("missing groups: %v", want)
	}
}

func TestRemoteMagpieKeepsEffortUpdates(t *testing.T) {
	effortHome(t)
	// 用公开协议形状的合成目录验证第二跳，不读取真实账号或请求。
	for _, manifest := range []string{
		`{"models":[{"slug":"m","supported_reasoning_levels":[],"shell_type":"unified_exec",
			"base_instructions":"Synthetic coding model.","supports_reasoning_effort_updates":true},
			{"slug":"n","supported_reasoning_levels":[],"shell_type":"unified_exec",
			"base_instructions":"Synthetic coding model.","supports_reasoning_effort_updates":false}]}`,
		`{"data":[{"id":"m","supports_reasoning_effort_updates":true},
			{"id":"n","supports_reasoning_effort_updates":false}]}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(manifest))
		}))
		models, err := catalog.FetchURL(context.Background(), server.URL+"/models", "synthetic-key", false, nil)
		server.Close()
		if err != nil {
			t.Fatal(err)
		}
		if len(models) != 2 {
			t.Fatalf("listed %d models, want 2", len(models))
		}
		entries := make([]Entry, 0, len(models))
		for _, model := range models {
			entries = append(entries, sharedEntry(&Provider{ID: "remote"}, model, settings.Settings{}))
		}
		wire, err := json.Marshal(map[string]any{"data": entries})
		if err != nil {
			t.Fatal(err)
		}
		second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(wire)
		}))
		relisted, err := catalog.FetchURL(context.Background(), second.URL+"/models", "synthetic-key", false, nil)
		second.Close()
		if err != nil {
			t.Fatal(err)
		}
		listed := make([]catalog.Model, 0, len(relisted))
		for _, model := range relisted {
			entry := sharedEntry(&Provider{ID: "second"}, model, settings.Settings{})
			listed = append(listed, codexListed([]Entry{entry}, func(string) []Member { return nil }, true)...)
		}
		var result struct {
			Models []struct {
				Slug    string `json:"slug"`
				Updates bool   `json:"supports_reasoning_effort_updates"`
			} `json:"models"`
		}
		if err := json.Unmarshal(codexcat.Catalog(listed), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Models) != 2 || !result.Models[0].Updates || result.Models[1].Updates {
			t.Fatalf("effort update capabilities lost across two gateways: %+v", result.Models)
		}
	}
}
