package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// gaiPlugin is a Google AI Studio plugin signed in, its models as its feed
// lists them (#3279): an embedding whose capabilities say "text": false —
// which the host's kinds drops, so the list says nothing of what it gives —
// and a TTS naming audio alone, beside models to talk with.
func gaiPlugin(t *testing.T) {
	t.Helper()
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", "")
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	spec := "@magpie-community/opencode-google-ai-studio"
	write := func(name string, v any) {
		t.Helper()
		b, _ := json.Marshal(v)
		os.MkdirAll(settings.Dir(), 0o700)
		if err := os.WriteFile(filepath.Join(settings.Dir(), name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("plugins.json", map[string]any{"plugins": []map[string]any{{"spec": spec}}})
	write("plugin-auth.json", map[string]map[string]any{"google-ai-studio": {"type": "api", "key": "sk-gai-1234"}})
	plugin.UseCached([]plugin.Provider{{ID: "google-ai-studio", Spec: spec, Name: "Google AI Studio",
		Methods:  []plugin.Method{{Type: "api", Label: "API key"}},
		Accounts: []plugin.Account{{Key: "google-ai-studio", Type: "api"}},
		Models: []plugin.Model{
			{ID: "gemini-embedding-001", Name: "Gemini Embedding 001", Inputs: []string{"text"}},
			{ID: "gemini-2.5-flash-preview-tts", Name: "Gemini TTS", Inputs: []string{"text"}, Outputs: []string{"audio"}},
			{ID: "gemini-2.5-flash", Name: "Gemini Flash", Outputs: []string{"text"}},
			{ID: "gemini-2.5-flash-native-audio", Name: "Native Audio", Outputs: []string{"text", "audio"}},
			{ID: "a-quiet-elder", Name: "Quiet"},
		}}})
	t.Cleanup(func() { plugin.UseCached(nil) })
}

// A model its plugin says gives nothing to talk with, or whose id names
// what doesn't, is no chat model: the catalog's own leave such a model out
// (textModel), but a plugin's list took every model as one (#3279: a Google
// AI Studio account's gemini-embedding-001 and its TTS sat in the chat
// list). A plugin naming no outputs keeps its model, and text among the
// outputs keeps it: what isn't said isn't guessed.
func TestPluginModelSayingNoTextLeavesTheChatList(t *testing.T) {
	pp := plugin.Provider{ID: "google-ai-studio", Models: []plugin.Model{
		{ID: "gemini-embedding-001", Name: "Gemini Embedding 001", Inputs: []string{"text"}},
		{ID: "gemini-2.5-flash-preview-tts", Name: "Gemini TTS", Inputs: []string{"text"}, Outputs: []string{"audio"}},
		{ID: "gemini-2.5-flash", Name: "Gemini Flash", Outputs: []string{"text"}},
		{ID: "gemini-2.5-flash-native-audio", Name: "Native Audio", Outputs: []string{"text", "audio"}},
		{ID: "a-quiet-elder", Name: "Quiet"},
	}}
	got := map[string]bool{}
	for _, m := range pluginCatalog(pp) {
		got[m.ID] = true
	}
	for _, gone := range []string{"gemini-embedding-001", "gemini-2.5-flash-preview-tts"} {
		if got[gone] {
			t.Errorf("%s: in the list, though the plugin says it gives nothing to talk with", gone)
		}
	}
	for _, kept := range []string{"gemini-2.5-flash", "gemini-2.5-flash-native-audio", "a-quiet-elder"} {
		if !got[kept] {
			t.Errorf("%s: lost from the list", kept)
		}
	}
}

// The account's list is what the chat chips, the agents' lists and the
// gateway build on (Available), and the embedding and the TTS are out of it
// there too, the models to talk with kept.
func TestPluginAccountListLeavesTheSpeechlessOut(t *testing.T) {
	gaiPlugin(t)
	p, err := Find("google-ai-studio")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, m := range p.Available() {
		got[m.ID] = true
	}
	for _, gone := range []string{"gemini-embedding-001", "gemini-2.5-flash-preview-tts"} {
		if got[gone] {
			t.Errorf("%s: in Available (the editor's chat chips)", gone)
		}
	}
	for _, kept := range []string{"gemini-2.5-flash", "gemini-2.5-flash-native-audio", "a-quiet-elder"} {
		if !got[kept] {
			t.Errorf("%s: lost from Available", kept)
		}
	}
}
