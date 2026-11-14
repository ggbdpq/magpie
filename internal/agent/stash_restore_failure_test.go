package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// failRestoreOnce makes the write that carries marker fail once: where the
// config file stood, a folder takes its place, so the write that would put
// the originals back cannot. It gives the config file back afterwards.
func failRestoreOnce(t *testing.T, path, marker string, fn func() error) (injected bool, err error) {
	t.Helper()
	real, rerr := filepath.EvalSymlinks(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	held := path + ".held"
	err = edit.Filtered(func(p string, data []byte) []byte {
		if p == real && bytes.Contains(data, []byte(marker)) && !injected {
			injected = true
			if rerr := os.Rename(path, held); rerr != nil {
				t.Logf("rename aside: %v", rerr)
			} else if merr := os.Mkdir(path, 0o700); merr != nil {
				t.Logf("mkdir: %v", merr)
			}
		}
		return data
	}, fn)
	if injected {
		if rerr := os.Remove(path); rerr != nil {
			t.Fatalf("undoing the injected folder: %v", rerr)
		}
		if rerr := os.Rename(held, path); rerr != nil {
			t.Fatalf("putting the config file back: %v", rerr)
		}
	}
	return injected, err
}

func stashHolds(t *testing.T, want ...string) {
	t.Helper()
	b, err := json.Marshal(stashShared())
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range want {
		if !strings.Contains(string(b), w) {
			t.Fatalf("the stash lost %q: %s", w, b)
		}
	}
}

// #4258: a Disconnect whose restore write fails leaves the stashed
// originals stashed for the next try — they are taken out only once the
// write that replaces them has gone through. A key another change wrote
// meanwhile is not the retry's to lose either.
func TestDisconnectFailureKeepsStashedOriginals(t *testing.T) {
	t.Run("claude", func(t *testing.T) {
		home := syncHome(t)
		path := filepath.Join(home, ".claude", "settings.json")
		writeFile(t, path, `{"model":"native-original","env":{"ANTHROPIC_BASE_URL":"https://original.example","ANTHROPIC_AUTH_TOKEN":"original-token"}}`)
		a := claude(home)
		if err := a.Connect(); err != nil {
			t.Fatal(err)
		}
		wired, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		injected, err := failRestoreOnce(t, path, "https://original.example", a.Disconnect)
		if !injected {
			t.Fatal("no write carried the original endpoint back")
		}
		if err == nil {
			t.Fatal("the restore write failed but Disconnect said it went through")
		}
		stashHolds(t, "https://original.example", "original-token", "native-original")

		// the write that failed is tried again on a wired config
		if err := os.WriteFile(path, wired, 0o644); err != nil {
			t.Fatal(err)
		}
		stash(map[string]string{"concurrent.probe": "keep"})
		if err := a.Disconnect(); err != nil {
			t.Fatalf("the retry: %v", err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range []string{"https://original.example", "original-token", "native-original"} {
			if !strings.Contains(string(body), w) {
				t.Fatalf("the retry lost %q:\n%s", w, body)
			}
		}
		if stashShared()["concurrent.probe"] != "keep" {
			t.Fatalf("the concurrent key went with the retry: %q", stashShared()["concurrent.probe"])
		}
	})

	t.Run("codex", func(t *testing.T) {
		home, _ := codexHome(t, "", "model = \"native-original\"\nmodel_provider = \"native-provider\"\n")
		path := filepath.Join(home, ".codex", "config.toml")
		cx := codex(home)
		if err := cx.Connect(); err != nil {
			t.Fatal(err)
		}
		wired, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		injected, err := failRestoreOnce(t, path, "native-provider", cx.Disconnect)
		if !injected {
			t.Fatal("no write carried the original provider back")
		}
		if err == nil {
			t.Fatal("the restore write failed but Disconnect said it went through")
		}
		stashHolds(t, "native-original", "native-provider")

		if err := os.WriteFile(path, wired, 0o644); err != nil {
			t.Fatal(err)
		}
		stash(map[string]string{"concurrent.probe": "keep"})
		if err := cx.Disconnect(); err != nil {
			t.Fatalf("the retry: %v", err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range []string{"native-provider", "native-original"} {
			if !strings.Contains(string(body), w) {
				t.Fatalf("the retry lost %q:\n%s", w, body)
			}
		}
		if stashShared()["concurrent.probe"] != "keep" {
			t.Fatalf("the concurrent key went with the retry: %q", stashShared()["concurrent.probe"])
		}
	})

	// #6601: the same shape in the other agents' restore paths — the
	// originals come out of the stash only once the write that puts them
	// back has gone through, so a write that fails leaves them for the
	// next try.
	t.Run("agy", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
		t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
		if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
		os.MkdirAll(filepath.Dir(path), 0o755)
		// wired by an older magpie: its model and provider in settings.json
		// itself, the user's own stashed for the move out
		os.WriteFile(path, []byte(`{"model": "magpie/deepseek/pro", "modelProvider": "gemini", "customModelsConfig": {"customModels": {"mine": {"apiProvider": "API_PROVIDER_ANTHROPIC", "modelName": "claude-sonnet-4-5"}, "magpie/deepseek/pro": {"apiProvider": "API_PROVIDER_GOOGLE_GEMINI", "modelName": "deepseek/pro"}}}}`), 0o644)
		stash(map[string]string{"agy:" + path + ":model": "mine", "agy:" + path + ":modelProvider": "vertex"})
		a := agy(home)

		// the restore write is the migrate a start runs: the next start
		// moves an older magpie's wiring out of settings.json
		injected, err := failRestoreOnce(t, path, "vertex", a.Sync)
		if !injected {
			t.Fatal("no write carried the user's provider back")
		}
		if err == nil {
			t.Fatal("the restore write failed but Sync said it went through")
		}
		stashHolds(t, "vertex", "mine")

		if err := os.WriteFile(path, []byte(`{"model": "magpie/deepseek/pro", "modelProvider": "gemini", "customModelsConfig": {"customModels": {"mine": {"apiProvider": "API_PROVIDER_ANTHROPIC", "modelName": "claude-sonnet-4-5"}, "magpie/deepseek/pro": {"apiProvider": "API_PROVIDER_GOOGLE_GEMINI", "modelName": "deepseek/pro"}}}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		stash(map[string]string{"concurrent.probe": "keep"})
		if err := a.Sync(); err != nil {
			t.Fatalf("the retry: %v", err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range []string{"vertex", "claude-sonnet-4-5"} {
			if !strings.Contains(string(body), w) {
				t.Fatalf("the retry lost %q:\n%s", w, body)
			}
		}
		if strings.Contains(string(body), "magpie/deepseek") {
			t.Fatalf("the retry left magpie's behind:\n%s", body)
		}
		if stashShared()["concurrent.probe"] != "keep" {
			t.Fatalf("the concurrent key went with the retry: %q", stashShared()["concurrent.probe"])
		}
	})

	t.Run("antigravity", func(t *testing.T) {
		home := antigravityHome(t)
		path := filepath.Join(home, ".antigravity", "settings.json")
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte(antigravityUser), 0o600)
		a := antigravity(home)
		if err := a.Apply("model", "magpie/anth/claude-sonnet-5"); err != nil {
			t.Fatal(err)
		}
		wired, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		injected, err := failRestoreOnce(t, path, "gateway.cymbal.com", a.Disconnect)
		if !injected {
			t.Fatal("no write carried the user's gateway back")
		}
		if err == nil {
			t.Fatal("the restore write failed but Disconnect said it went through")
		}
		stashHolds(t, "gateway.cymbal.com", "gemini-3.8-flash")

		if err := os.WriteFile(path, wired, 0o600); err != nil {
			t.Fatal(err)
		}
		stash(map[string]string{"concurrent.probe": "keep"})
		if err := a.Disconnect(); err != nil {
			t.Fatalf("the retry: %v", err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range []string{"gateway.cymbal.com", "gemini-3.8-flash"} {
			if !strings.Contains(string(body), w) {
				t.Fatalf("the retry lost %q:\n%s", w, body)
			}
		}
		if stashShared()["concurrent.probe"] != "keep" {
			t.Fatalf("the concurrent key went with the retry: %q", stashShared()["concurrent.probe"])
		}
	})

	t.Run("ante", func(t *testing.T) {
		home, _, settings := anteHome(t)
		a := ante(home)
		if err := a.Connect(); err != nil {
			t.Fatal(err)
		}
		wired, err := os.ReadFile(settings)
		if err != nil {
			t.Fatal(err)
		}

		injected, err := failRestoreOnce(t, settings, "together", a.Disconnect)
		if !injected {
			t.Fatal("no write carried the user's provider back")
		}
		if err == nil {
			t.Fatal("the restore write failed but Disconnect said it went through")
		}
		stashHolds(t, "together", "moonshotai/Kimi-K2-Instruct")

		if err := os.WriteFile(settings, wired, 0o644); err != nil {
			t.Fatal(err)
		}
		stash(map[string]string{"concurrent.probe": "keep"})
		if err := a.Disconnect(); err != nil {
			t.Fatalf("the retry: %v", err)
		}
		body, err := os.ReadFile(settings)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range []string{"together", "moonshotai/Kimi-K2-Instruct"} {
			if !strings.Contains(string(body), w) {
				t.Fatalf("the retry lost %q:\n%s", w, body)
			}
		}
		if stashShared()["concurrent.probe"] != "keep" {
			t.Fatalf("the concurrent key went with the retry: %q", stashShared()["concurrent.probe"])
		}
	})

	t.Run("astrbot", func(t *testing.T) {
		home, path := astrbotHome(t, astrbotConfig)
		a := astrbot(home)
		if err := a.Pick("model", "magpie/kimi/k2"); err != nil {
			t.Fatal(err)
		}
		wired, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		// the entry the restore puts back: the pick itself, whose value
		// also stands in connect.was — so the entry is named by its key
		picked := ""
		for k, v := range stashShared() {
			if strings.Contains(k, "provider_id") && v == "deepseek/deepseek-chat" {
				picked = k
			}
		}
		if picked == "" {
			t.Fatalf("the user's pick is not stashed: %v", stashShared())
		}

		injected, err := failRestoreOnce(t, path, "deepseek/deepseek-chat", a.Disconnect)
		if !injected {
			t.Fatal("no write carried the user's pick back")
		}
		if err == nil {
			t.Fatal("the restore write failed but Disconnect said it went through")
		}
		if stashShared()[picked] != "deepseek/deepseek-chat" {
			t.Fatalf("the stash lost the user's pick (%s): %v", picked, stashShared())
		}

		if err := os.WriteFile(path, wired, 0o644); err != nil {
			t.Fatal(err)
		}
		stash(map[string]string{"concurrent.probe": "keep"})
		if err := a.Disconnect(); err != nil {
			t.Fatalf("the retry: %v", err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != astrbotConfig {
			t.Fatalf("the retry left:\n%s", body)
		}
		if stashShared()["concurrent.probe"] != "keep" {
			t.Fatalf("the concurrent key went with the retry: %q", stashShared()["concurrent.probe"])
		}
	})

	t.Run("cline", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		t.Setenv("CLINE_DIR", "")
		t.Setenv("CLINE_DATA_DIR", "")
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
		t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
		if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
			t.Fatal(err)
		}
		data := filepath.Join(home, ".cline", "data")
		path := filepath.Join(data, "settings", "providers.json")
		modelsPath := filepath.Join(data, "settings", "models.json")
		statePath, secretsPath := filepath.Join(data, "globalState.json"), filepath.Join(data, "secrets.json")
		os.MkdirAll(filepath.Dir(path), 0o700)
		os.WriteFile(modelsPath, []byte(`{"version":1,"providers":{"openai-compatible":{"provider":{"name":"OpenAI Compatible","baseUrl":"https://x/v1","defaultModelId":"mine"},"models":{"mine":{"id":"mine","name":"mine"}}},"ollama":{"models":{"q":{"id":"q"}}}}}`), 0o600)
		os.WriteFile(path, []byte(`{"version":1,"lastUsedProvider":"anthropic","modes":{},"providers":{
  "anthropic":{"settings":{"provider":"anthropic","apiKey":"sk-a","model":"claude-opus-5","reasoning":{"effort":"high"}},"updatedAt":"2026-09-01T00:00:00.000Z","tokenSource":"manual"},
  "openai-compatible":{"settings":{"provider":"openai-compatible","apiKey":"sk-o","model":"mine","baseUrl":"https://x/v1"},"updatedAt":"2026-09-01T00:00:00.000Z","tokenSource":"manual"}}}`), 0o600)
		os.WriteFile(statePath, []byte(`{"actModeApiProvider":"anthropic","planModeApiProvider":"anthropic","actModeOpenAiModelId":"mine",`+
			`"actModeOpenAiModelInfo":{"maxTokens":10},"openAiBaseUrl":"https://x/v1","telemetrySetting":"disabled"}`), 0o600)
		os.WriteFile(secretsPath, []byte(`{"openAiApiKey":"sk-o","anthropicApiKey":"sk-a"}`), 0o600)
		a := cline(home)
		if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
			t.Fatal(err)
		}
		wired, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatal(err)
		}
		// the entries the restore puts back, named by their keys: the
		// stashed entry and secret also carry the state's own markers
		keys := []string{"state", "secret", "stated"}
		for _, k := range keys {
			if stashShared()["cline:"+path+":"+k] == "" {
				t.Fatalf("the extension's %s is not stashed: %v", k, stashShared())
			}
		}

		injected, err := failRestoreOnce(t, statePath, "https://x/v1", a.Disconnect)
		if !injected {
			t.Fatal("no write carried the extension's state back")
		}
		if err == nil {
			t.Fatal("the restore write failed but Disconnect said it went through")
		}
		for _, k := range keys {
			if stashShared()["cline:"+path+":"+k] == "" {
				t.Fatalf("the stash lost the extension's %s: %v", k, stashShared())
			}
		}

		if err := os.WriteFile(statePath, wired, 0o644); err != nil {
			t.Fatal(err)
		}
		stash(map[string]string{"concurrent.probe": "keep"})
		if err := a.Disconnect(); err != nil {
			t.Fatalf("the retry: %v", err)
		}
		body, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "https://x/v1") {
			t.Fatalf("the retry lost the extension's state:\n%s", body)
		}
		providers, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(providers), `"anthropic"`) {
			t.Fatalf("the retry lost the provider in use:\n%s", providers)
		}
		if stashShared()["concurrent.probe"] != "keep" {
			t.Fatalf("the concurrent key went with the retry: %q", stashShared()["concurrent.probe"])
		}
	})

	t.Run("claudedesktop", func(t *testing.T) {
		home, p := desktopSandbox(t)
		desktopWrite(t, p.config, `{"deploymentMode":"1p","preferences":{"x":1}}`)
		desktopWrite(t, p.config3p, `{"deploymentMode":"1p","preferences":{"x":1}}`)
		a := claudeDesktop(home)
		if err := a.Field("provider").Set("magpie"); err != nil {
			t.Fatal(err)
		}
		wired, err := os.ReadFile(p.config)
		if err != nil {
			t.Fatal(err)
		}

		injected, err := failRestoreOnce(t, p.config, `"1p"`, func() error { return a.Field("provider").Set("") })
		if !injected {
			t.Fatal("no write carried the user's mode back")
		}
		if err == nil {
			t.Fatal("the restore write failed but Disconnect said it went through")
		}
		stashHolds(t, `"1p"`)

		if err := os.WriteFile(p.config, wired, 0o644); err != nil {
			t.Fatal(err)
		}
		stash(map[string]string{"concurrent.probe": "keep"})
		if err := a.Field("provider").Set(""); err != nil {
			t.Fatalf("the retry: %v", err)
		}
		if m := desktopJSON(t, p.config); m["deploymentMode"] != "1p" {
			t.Fatalf("the retry left: %v", m)
		}
		if stashShared()["concurrent.probe"] != "keep" {
			t.Fatalf("the concurrent key went with the retry: %q", stashShared()["concurrent.probe"])
		}
	})
}
