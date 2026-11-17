package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// thewlake (#6961): agy runs at its own 256k context limit however the UI
// sets the model's window, because magpie-settings.json's custom models
// carry only apiProvider and modelName. The window the UI sets goes into
// each entry as maxTokens; a model with no window set stays as it was.
func TestAgyWritesTheModelsWindow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}, Contexts: map[string]int{"pro": 512000}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte("{}"), 0o644)
	a, err := Find("agy")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	over := filepath.Join(filepath.Dir(path), "magpie-settings.json")
	b, err := os.ReadFile(over)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		CustomModelsConfig struct {
			CustomModels map[string]struct {
				ApiProvider string `json:"apiProvider"`
				ModelName   string `json:"modelName"`
				MaxTokens   *int   `json:"maxTokens"`
			} `json:"customModels"`
		} `json:"customModelsConfig"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	cm := m.CustomModelsConfig.CustomModels
	pro := cm["magpie/deepseek/pro"]
	if pro.MaxTokens == nil || *pro.MaxTokens != 512000 {
		t.Fatalf("pro carries no window: %+v", pro)
	}
	if fl := cm["magpie/deepseek/flash"]; fl.MaxTokens != nil {
		t.Fatalf("flash, with no window set, carries %d", *fl.MaxTokens)
	}
}
