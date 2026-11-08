package gui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// Fish-under-sea (#5418): a provider's editor lists its speech models
// (TTS and transcription) and its embedding models (embedding and
// rerank) apart, as its image and video ones are; those unticked there
// are saved as hiddenSpeech and hiddenEmbeddings, kept by a Save that
// leaves them out and cleared by [], and a save of one leaves the other's
// alone.
func TestSpeechAndEmbeddingModelsHidden(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(body string) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/save", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
	const base = `"id":"films","name":"Films","key":"sk-f","chat":"https://films.example/v1","icon":"generic"`
	post(`{` + base + `,"new":true}`)
	if err := catalog.SaveLive("films", "https://films.example/v1", []catalog.Model{
		{ID: "text"},
		{ID: "tts-a", Serves: catalog.ServesSpeech},
		{ID: "tts-b", Serves: catalog.ServesSpeech},
		{ID: "wh-a", Serves: catalog.ServesTranscription},
		{ID: "emb-a", Serves: catalog.ServesEmbedding},
		{ID: "rer-a", Serves: catalog.ServesRerank},
	}); err != nil {
		t.Fatal(err)
	}
	info := func() providerJSON {
		t.Helper()
		p, err := provider.Find("films")
		if err != nil {
			t.Fatal(err)
		}
		return providerInfo(*p, nil)
	}
	got := info()
	if !slices.Equal(got.SpeechIDs, []string{"tts-a", "tts-b", "wh-a"}) {
		t.Fatalf("editor's speech models %v", got.SpeechIDs)
	}
	if !slices.Equal(got.EmbedIDs, []string{"emb-a", "rer-a"}) {
		t.Fatalf("editor's embedding models %v", got.EmbedIDs)
	}

	post(`{` + base + `,"from":"films","models":[],"headers":{},"hiddenSpeech":["tts-a"],"hiddenEmbeddings":["emb-a"]}`)
	if got := info(); !slices.Equal(got.HiddenSpeech, []string{"tts-a"}) || !slices.Equal(got.HiddenEmbeddings, []string{"emb-a"}) {
		t.Fatalf("saved speech %v, embeddings %v", got.HiddenSpeech, got.HiddenEmbeddings)
	}
	post(`{` + base + `,"from":"films","models":[],"headers":{}}`)
	if got := info(); !slices.Equal(got.HiddenSpeech, []string{"tts-a"}) || !slices.Equal(got.HiddenEmbeddings, []string{"emb-a"}) {
		t.Fatalf("a Save that left them out changed them: %v, %v", got.HiddenSpeech, got.HiddenEmbeddings)
	}
	post(`{` + base + `,"from":"films","models":[],"headers":{},"hiddenSpeech":[],"hiddenEmbeddings":[]}`)
	if got := info(); len(got.HiddenSpeech) != 0 || len(got.HiddenEmbeddings) != 0 {
		t.Fatalf("[] kept speech %v, embeddings %v", got.HiddenSpeech, got.HiddenEmbeddings)
	}
	post(`{` + base + `,"from":"films","models":[],"headers":{},"hiddenSpeech":["tts-b"]}`)
	if got := info(); !slices.Equal(got.HiddenSpeech, []string{"tts-b"}) || !slices.Equal(got.HiddenEmbeddings, []string{}) {
		t.Fatalf("a save of one changed the other's: %v, %v", got.HiddenSpeech, got.HiddenEmbeddings)
	}
}
