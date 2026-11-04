package gui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #4282: the editor knows nothing of the most bytes one request to a
// provider may take, set with `magpie provider body-limit <id> 28mib`,
// so its save keeps it.
func TestSaveKeepsBodyLimit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "limit-test", Name: "Limit", Chat: "https://example.invalid/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetBodyLimit("limit-test", 28<<20); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/save", strings.NewReader(`{"id":"limit-test","name":"Limit","chat":"https://example.invalid/v1"}`)))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	got, err := provider.Find("limit-test")
	if err != nil {
		t.Fatal(err)
	}
	if got.BodyLimit != 28<<20 || got.Name != "Limit" {
		t.Fatalf("the save dropped the body limit %d", got.BodyLimit)
	}
}
