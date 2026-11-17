package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGeminiRelayModelBodyProbe(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	seen := make(chan string, 2)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Model string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		want := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1beta/models/"), ":generateContent")
		if body.Model != want {
			http.Error(w, `{"error":{"message":"Model is unsupported and no wildcard routing rule is configured"}}`, http.StatusBadRequest)
			return
		}
		seen <- body.Model
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"OK"}]},"finishReason":"STOP"}]}`))
	}))
	t.Cleanup(up.Close)
	p := Provider{ID: "relay", Name: "Relay", Key: "relay-key", Gemini: up.URL + "/v1beta", GeminiModelInBody: true, Models: []string{"gemini-one", "gemini-two"}}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	if err := SetUpstreamName("relay/gemini-two", "vendor/gemini-two"); err != nil {
		t.Fatal(err)
	}
	for _, result := range p.TestModels(context.Background(), p.Models) {
		if !result.OK {
			t.Fatalf("%s: status %d: %s", result.Model, result.Status, result.Error)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("%d probes reached the relay, want two", len(seen))
	}
}

func TestGeminiRelayModelBodyStaysOutOfOtherWires(t *testing.T) {
	for _, p := range []Provider{
		{Gemini: "https://generativelanguage.googleapis.com/v1beta"},
		{GeminiModelInBody: true, Responses: "https://relay.example/v1"},
		{GeminiModelInBody: true, Gemini: "https://relay.example/v1beta", Account: &Account{Agent: "gemini"}},
		{Preset: VertexPreset, GeminiModelInBody: true, Gemini: "https://relay.example/v1beta", Vertex: &Vertex{Project: "project"}},
	} {
		if p.SendsGeminiModel() {
			t.Fatalf("an unrelated wire sends the Gemini model: %+v", p)
		}
	}
}
