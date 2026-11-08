package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A vendor's list keeps its models made for another API, each marked with
// the API (#5339): an embedding, a rerank, a speech and a transcription
// model. They were dropped before, so no route could find them under a
// group. They stay out of the chat models (Chat), and a moderation model,
// which no route of magpie's serves, is still dropped.
func TestFetchKeepsModelsOfOtherAPIs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Write([]byte(`{"object":"list","data":[{"id":"chat-model"},{"id":"text-embedding-3-small"},{"id":"BAAI/bge-reranker-v2-m3"},{"id":"tts-1"},{"id":"whisper-1"},{"id":"omni-moderation-latest"}]}`))
	}))
	defer srv.Close()
	ms, _, err := FetchAt(context.Background(), srv.URL+"/v1", "k", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, m := range ms {
		got[m.ID] = m.Serves
	}
	want := map[string]string{
		"chat-model":              "",
		"text-embedding-3-small":  ServesEmbedding,
		"BAAI/bge-reranker-v2-m3": ServesRerank,
		"tts-1":                   ServesSpeech,
		"whisper-1":               ServesTranscription,
	}
	if len(got) != len(want) {
		t.Fatalf("kept %v, want %v", got, want)
	}
	for id, s := range want {
		if v, ok := got[id]; !ok || v != s {
			t.Errorf("%s: %q %v, want %q", id, v, ok, s)
		}
	}
	if c := Chat(ms); len(c) != 1 || c[0].ID != "chat-model" {
		t.Errorf("Chat %v", c)
	}
}

// A saved list's models of other APIs are found by API, and the list says
// it kept them, so a list saved before they were kept is known by it.
func TestLiveServingByAPI(t *testing.T) {
	ms := []Model{{ID: "chat-model"}, {ID: "embed-1", Serves: ServesEmbedding}, {ID: "tts-1", Serves: ServesSpeech}}
	if err := SaveLive("kinds-kp", "http://x/v1", ms); err != nil {
		t.Fatal(err)
	}
	if !LiveKinded("kinds-kp") || LiveKinded("none") {
		t.Fatal("LiveKinded")
	}
	var all []string
	for _, m := range LiveServing("kinds-kp", "") {
		all = append(all, m.ID)
	}
	if strings.Join(all, ",") != "embed-1,tts-1" {
		t.Fatalf("LiveServing any: %v", all)
	}
	if e := LiveServing("kinds-kp", ServesEmbedding); len(e) != 1 || e[0].ID != "embed-1" {
		t.Fatalf("LiveServing embedding: %v", e)
	}
	if live, _, _ := Live("kinds-kp"); len(Chat(live)) != 1 {
		t.Fatalf("Chat of the saved list: %v", Chat(live))
	}
}

func TestServesOf(t *testing.T) {
	for id, want := range map[string]string{
		"text-embedding-3-large": ServesEmbedding, "BAAI/bge-m3": ServesEmbedding, "Qwen/Qwen3-Embedding-8B": ServesEmbedding,
		"bge-reranker-v2-m3": ServesRerank, "jina-reranker-m0": ServesRerank,
		"tts-1-hd": ServesSpeech, "gpt-4o-mini-tts": ServesSpeech,
		"whisper-1": ServesTranscription, "gpt-4o-transcribe": ServesTranscription,
		"gpt-5.5": "", "claude-sonnet-5": "", "embedder-chat": ServesEmbedding,
		// Voyage AI's, which name no "embed" (heatonz on Discord)
		"voyage-4-large": ServesEmbedding, "voyage-code-3": ServesEmbedding, "voyage/voyage-3.5-lite": ServesEmbedding,
		"rerank-3": ServesRerank, "rerank-2.5-lite": ServesRerank,
		// and the families whose ids name no embed either (#5416)
		"gte-Qwen2-7B-instruct": ServesEmbedding, "gte-large": ServesEmbedding,
		"e5-large-v2": ServesEmbedding, "m3e-base": ServesEmbedding, "text2vec-base-chinese": ServesEmbedding,
	} {
		if got := ServesOf(id); got != want {
			t.Errorf("ServesOf(%q) = %q, want %q", id, got, want)
		}
	}
}

// A vendor's list that says what each model is for ("model_type",
// Xinference's) is read where the id alone says less: the list's word is
// the model's Serves, a kind no route of magpie's serves leaves the id's
// word, and one the list says nothing of is read from the id (#5416).
func TestFetchReadsTheKindTheListSays(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Write([]byte(`{"object":"list","data":[` +
			`{"id":"gte-base","model_type":"embedding"},` +
			`{"id":"my-ranker","model_type":"rerank"},` +
			`{"id":"qwen3","model_type":"LLM"},` +
			`{"id":"BAAI/bge-reranker-v2-m3"},` +
			`{"id":"m3e-base"}]}`))
	}))
	defer srv.Close()
	ms, _, err := FetchAt(context.Background(), srv.URL+"/v1", "k", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, m := range ms {
		got[m.ID] = m.Serves
	}
	want := map[string]string{
		"gte-base":                ServesEmbedding,
		"my-ranker":               ServesRerank,
		"qwen3":                   "",
		"BAAI/bge-reranker-v2-m3": ServesRerank,
		"m3e-base":                ServesEmbedding,
	}
	if len(got) != len(want) {
		t.Fatalf("kept %v, want %v", got, want)
	}
	for id, s := range want {
		if v, ok := got[id]; !ok || v != s {
			t.Errorf("%s: %q %v, want %q", id, v, ok, s)
		}
	}
	if c := Chat(ms); len(c) != 1 || c[0].ID != "qwen3" {
		t.Errorf("Chat %v", c)
	}
}
