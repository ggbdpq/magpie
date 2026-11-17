package gui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestGeminiRelayModelBodySaved(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	for _, step := range []struct {
		setting string
		want    bool
	}{
		{`,"geminiModelInBody":true`, true},
		{"", true},
		{`,"geminiModelInBody":false`, false},
	} {
		w := httptest.NewRecorder()
		body := `{"id":"relay","name":"Relay","key":"relay-key","gemini":"http://127.0.0.1:1/v1beta","models":["gemini-one"]` + step.setting + `}`
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/save", strings.NewReader(body)))
		if w.Code != http.StatusOK {
			t.Fatalf("save: %d %s", w.Code, w.Body)
		}
		p, err := provider.Find("relay")
		if err != nil {
			t.Fatal(err)
		}
		if p.GeminiModelInBody != step.want || providerInfo(*p, nil).GeminiModelInBody != step.want {
			t.Fatalf("setting %q: stored=%v, want %v", step.setting, p.GeminiModelInBody, step.want)
		}
	}
}

func TestGeminiRelayModelBodyTyped(t *testing.T) {
	p := provider.Provider{Gemini: "https://relay.example/v1beta", GeminiModelInBody: true}
	for _, value := range []*bool{nil, new(false), new(true)} {
		got := typed(p, provider.Provider{}, nil, nil, nil, value)
		want := value == nil || *value
		if got.GeminiModelInBody != want {
			t.Fatalf("unsaved test: body=%v, want %v", got.GeminiModelInBody, want)
		}
	}
}
