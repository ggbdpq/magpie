package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Fish-under-sea (#5418): a provider's own /v1/models lists whisper-1,
// tts-1 and an embedding model next to its chat ones, and magpie's own
// list said nothing of what a model is for, so an SDK reading a flat list
// had to guess from ids. The models made for another API are listed only
// when the caller asks for their kind (?kind=embedding, rerank, speech,
// transcription, comma separated), each marked by its "kind", as image
// and video models are: an agent that offers every id its list gives as a
// model to chat with would offer a TTS. Those unticked in their
// provider's editor (hiddenSpeech, hiddenEmbeddings) are left out of the
// list, and a request that names one still serves.
func TestModelsListServingKindsWhenAsked(t *testing.T) {
	fresh(t)
	busy, free := newKindVendor(t, true), newKindVendor(t, false)
	saveFetched(t, provider.Provider{ID: "busy", Name: "Busy", Chat: busy.URL + "/v1", Key: "k", Models: []string{"chat-model"}})
	saveFetched(t, provider.Provider{ID: "free", Name: "Free", Chat: free.URL + "/v1", Key: "k", Models: []string{"chat-model"}})
	h := New().Handler()
	rows := func(query string) map[string]string {
		t.Helper()
		if !strings.HasPrefix(query, "/v1/models") {
			query = "/v1/models" + query
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", query, nil))
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", query, rec.Code, rec.Body.String())
		}
		var l struct {
			Data []struct {
				ID   string
				Kind string
			}
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil {
			t.Fatalf("%s: %v\n%s", query, err, rec.Body.String())
		}
		out := map[string]string{}
		for _, m := range l.Data {
			out[m.ID] = m.Kind
		}
		return out
	}
	plain := rows("/v1/models")
	for _, id := range []string{"busy/embed-1", "free/embed-1", "busy/tts-1", "free/whisper-1", "free/bge-reranker-1"} {
		if _, ok := plain[id]; ok {
			t.Errorf("the plain list offers %s", id)
		}
	}
	want := []struct{ query, id, kind string }{
		{"?kind=embedding", "busy/embed-1", "embedding"},
		{"?kind=embedding", "free/embed-1", "embedding"},
		{"?kind=rerank", "busy/bge-reranker-1", "rerank"},
		{"?kind=rerank", "free/bge-reranker-1", "rerank"},
		{"?kind=speech", "busy/tts-1", "speech"},
		{"?kind=speech", "free/tts-1", "speech"},
		{"?kind=transcription", "busy/whisper-1", "transcription"},
		{"?kind=transcription", "free/whisper-1", "transcription"},
		{"?kind=speech,transcription", "busy/tts-1", "speech"},
		{"?kind=speech,transcription", "free/whisper-1", "transcription"},
	}
	for _, w := range want {
		got := rows(w.query)
		if got[w.id] != w.kind {
			t.Errorf("%s lists %s as kind %q, want %q (list %v)", w.query, w.id, got[w.id], w.kind, got)
		}
	}
	if got := rows("?kind=speech"); got["busy/whisper-1"] != "" {
		t.Errorf("a transcription model listed as speech: %v", got)
	}
	badRec := httptest.NewRecorder()
	h.ServeHTTP(badRec, httptest.NewRequest("GET", "/v1/models?kind=decide", nil))
	if badRec.Code != 400 {
		t.Errorf("an unknown kind: %d %s", badRec.Code, badRec.Body.String())
	}

	// those unticked in the editor are not listed, and the rest still is
	p, err := provider.Find("busy")
	if err != nil {
		t.Fatal(err)
	}
	p.HiddenSpeech = []string{"tts-1"}
	p.HiddenEmbeddings = []string{"embed-1"}
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	if got := rows("?kind=speech"); got["busy/tts-1"] != "" || got["free/tts-1"] != "speech" {
		t.Errorf("the hidden speech model still listed: %v", got)
	}
	if got := rows("?kind=embedding"); got["busy/embed-1"] != "" || got["free/embed-1"] != "embedding" {
		t.Errorf("the hidden embedding still listed: %v", got)
	}
	if got := rows("?kind=transcription"); got["busy/whisper-1"] != "transcription" {
		t.Errorf("the unticked one's kindmates lost: %v", got)
	}
}

// Fish-under-sea (#5418): a model unticked in its provider's editor still
// serves when a request names it, as a hidden image model still draws
// (OfferedDrawers, DrawerHidden).
func TestHiddenServingModelStillServes(t *testing.T) {
	fresh(t)
	free := newKindVendor(t, false)
	saveFetched(t, provider.Provider{ID: "solo", Name: "Solo", Chat: free.URL + "/v1", Key: "k", Models: []string{"chat-model"}, HiddenEmbeddings: []string{"embed-1"}})
	s := New()
	if code, raw := postRetrieval(t, s, "/v1/embeddings", `{"model":"embed-1","input":"hi"}`); code != 200 || !strings.Contains(raw, `[0.5,0.25]`) {
		t.Fatalf("embeddings by a hidden model's id: %d %s", code, raw)
	}
	for _, asked := range free.log() {
		if asked == "POST /v1/embeddings" {
			return
		}
	}
	t.Fatalf("the vendor was asked %v", free.log())
}
