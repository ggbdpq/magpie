package provider

import (
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// Fish-under-sea (#5418): a plugin that says what its models give gets
// each one its section without a new API. What #3279 drops from the chat
// lists — an embedding ({"text": false}) and a TTS (audio alone) — is
// kept apart instead, tagged with its API (Model.Serves), so it sits in
// its editor section, is listed on /v1/models with its kind, and is
// found on its API's route. A model said nothing of stays out of all of
// that whatever its id (embedder-chat), and an image keeps its place
// (#2792's to place), as the speechless tests hold.
func TestPluginServingMapsWhatThePluginSays(t *testing.T) {
	gaiPlugin(t)
	p, err := Find("google-ai-studio")
	if err != nil {
		t.Fatal(err)
	}
	ids := func(kind string) []string {
		t.Helper()
		var out []string
		for _, m := range PluginServing(*p, kind) {
			out = append(out, m.ID)
		}
		return out
	}
	if got := ids(catalog.ServesEmbedding); !slices.Equal(got, []string{"gemini-embedding-001", "bge-m3"}) {
		t.Fatalf("embeddings %v", got)
	}
	if got := ids(catalog.ServesSpeech); !slices.Equal(got, []string{"gemini-2.5-flash-preview-tts"}) {
		t.Fatalf("speech %v", got)
	}
	if got := ids(catalog.ServesRerank); len(got) != 0 {
		t.Fatalf("rerank %v", got)
	}
	if got := ids(""); !slices.Equal(got, []string{"gemini-embedding-001", "bge-m3", "gemini-2.5-flash-preview-tts"}) {
		t.Fatalf("every serving model %v", got)
	}
}
