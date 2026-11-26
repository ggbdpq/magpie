package provider

import (
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/plugin"
)

// #7889: an Anthropic plugin (such as agy-cli from 0.1.141) that also lists an
// image-only model on @ai-sdk/openai-compatible (outputs: ["image"], no "text")
// does not speak /v1/chat/completions for text models. Setting p.Chat from that
// non-text model caused unlisted or variant model ids (e.g.
// gemini-4-hydrogen-medium) to look relayable on Chat and bypass translation.
func TestPluginNonTextModelsDoNotEnableChatProtocol(t *testing.T) {
	agy := plugin.Provider{
		ID:   "agy-cli",
		Name: "Antigravity (local agy CLI)",
		NPM:  "@ai-sdk/anthropic",
		Models: []plugin.Model{
			{
				ID:          "gemini-4-hydrogen",
				NPM:         "@ai-sdk/anthropic",
				Outputs:     []string{"text"},
				OutputsSaid: true,
			},
			{
				ID:          "gemini-3.1-flash-image",
				NPM:         "@ai-sdk/openai-compatible",
				Outputs:     []string{"image"},
				OutputsSaid: true,
			},
		},
	}
	p := pluginProvider(agy, pluginLogin{})
	if got := p.Speaks(); !slices.Equal(got, []Protocol{Anthropic}) {
		t.Fatalf("agy-cli Speaks() = %v, want [%s]", got, Anthropic)
	}
	if p.Base(Chat) != "" {
		t.Fatalf("agy-cli Base(Chat) = %q, want empty", p.Base(Chat))
	}
	if !p.PluginOpenAICompatible("gemini-3.1-flash-image") {
		t.Fatalf("PluginOpenAICompatible(gemini-3.1-flash-image) = false, want true")
	}
	if got := p.OpenAIBase(); got != pluginBase+"agy-cli/v1" {
		t.Fatalf("OpenAIBase() = %q, want %q", got, pluginBase+"agy-cli/v1")
	}

	// Non-text video/embedding models must likewise not enable Chat.
	videoOnly := plugin.Provider{
		ID:  "anth-video",
		NPM: "@ai-sdk/anthropic",
		Models: []plugin.Model{
			{ID: "claude-1", NPM: "@ai-sdk/anthropic", Outputs: []string{"text"}, OutputsSaid: true},
			{ID: "vid-1", NPM: "@ai-sdk/openai-compatible", Outputs: []string{"video"}, OutputsSaid: true},
			{ID: "emb-1", NPM: "@ai-sdk/openai-compatible", Outputs: nil, OutputsSaid: true},
		},
	}
	if got := pluginProvider(videoOnly, pluginLogin{}).Speaks(); !slices.Equal(got, []Protocol{Anthropic}) {
		t.Fatalf("anth-video Speaks() = %v, want [%s]", got, Anthropic)
	}

	// Text, multimodal text+image, and unspecified-output Chat models still
	// enable Chat as before.
	for _, tc := range []struct {
		name string
		m    plugin.Model
	}{
		{"unspecified outputs", plugin.Model{ID: "chat-1", NPM: "@ai-sdk/openai-compatible"}},
		{"text output", plugin.Model{ID: "chat-2", NPM: "@ai-sdk/openai-compatible", Outputs: []string{"text"}, OutputsSaid: true}},
		{"text+image output", plugin.Model{ID: "chat-3", NPM: "@ai-sdk/openai-compatible", Outputs: []string{"text", "image"}, OutputsSaid: true}},
	} {
		pp := plugin.Provider{ID: "mixed", Models: []plugin.Model{tc.m}}
		if got := pluginProvider(pp, pluginLogin{}).Speaks(); !slices.Equal(got, []Protocol{Chat}) {
			t.Errorf("%s: Speaks() = %v, want [%s]", tc.name, got, Chat)
		}
	}

	// An image-only Gemini (@ai-sdk/google) model still keeps CodeAssist so
	// drawCodeAssist remains reachable.
	gemImg := plugin.Provider{
		ID: "gem-draw",
		Models: []plugin.Model{
			{ID: "gemini-img", NPM: "@ai-sdk/google", Outputs: []string{"image"}, OutputsSaid: true},
		},
	}
	if got := pluginProvider(gemImg, pluginLogin{}).Speaks(); !slices.Equal(got, []Protocol{CodeAssist}) {
		t.Fatalf("gem-draw Speaks() = %v, want [%s]", got, CodeAssist)
	}
}
