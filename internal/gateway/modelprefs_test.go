package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// The name the user gave a provider's model, and the reasoning levels they
// kept of it, are what /v1/models lists; the same model from another
// provider keeps its own, and the id stays the same.
func TestModelsListUsersNameAndLevels(t *testing.T) {
	fresh(t)
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	data := `{"a":{"models":{"sol":{"id":"sol","name":"Sol","reasoning_options":[{"type":"effort","values":["low","medium","high","max"]}]}}},
		"b":{"models":{"sol":{"id":"sol","name":"Sol","reasoning_options":[{"type":"effort","values":["low","medium","high","max"]}]}}}}`
	if err := os.WriteFile(catalog.CachePath(), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	for _, id := range []string{"a", "b"} {
		if err := provider.Save(provider.Provider{ID: id, Name: strings.ToUpper(id), Catalog: id, Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"sol"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SetModelName("a/sol", "Sol 5"); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelEfforts("a/sol", []string{"low", "high"}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	var out struct {
		Data []struct {
			ID     string `json:"id"`
			Name   string `json:"display_name"`
			Levels []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, m := range out.Data {
		var ls []string
		for _, l := range m.Levels {
			ls = append(ls, l.Effort)
		}
		got[m.ID] = m.Name + " " + strings.Join(ls, ",")
	}
	if got["a/sol"] != "Sol 5 low,high" || got["b/sol"] != "Sol low,medium,high,max" {
		t.Fatalf("%v\n%s", got, rec.Body.String())
	}
}

// A custom provider whose vendor's list is silent about a model's levels
// had them published only while the catalog's borrowed word held, and an
// override naming exactly those levels was folded away as "offered all
// again", leaving nothing kept to publish (#5458). What the user names is
// kept now even when it is every level the model has, and the list
// publishes it.
func TestModelsListKeepsAnOverrideNamingAllLevels(t *testing.T) {
	fresh(t)
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	data := `{"deepseek":{"models":{"deepseek-flash":{"id":"deepseek-flash","name":"DeepSeek Flash",
		"reasoning_options":[{"type":"effort","values":["low","high","max"]}]}}}}`
	if err := os.WriteFile(catalog.CachePath(), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []map[string]any{
			{"id": "deepseek-flash", "object": "model", "owned_by": "relay"},
		}})
	}))
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "digua", Name: "Digua", Chat: srv.URL + "/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("digua")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := p.RefetchTaking(context.Background()); err != nil {
		t.Fatal(err)
	}
	// the levels the CLI reports, named whole and in the user's order
	if err := provider.SetModelEfforts("digua/deepseek-flash", []string{"max", "high", "low"}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	var out struct {
		Data []struct {
			ID     string   `json:"id"`
			Flat   []string `json:"reasoning_levels"`
			Levels []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for _, m := range out.Data {
		if m.ID != "digua/deepseek-flash" {
			continue
		}
		if got, want := strings.Join(m.Flat, ","), "low,high,max"; got != want {
			t.Fatalf("%s: reasoning_levels %q, want %q\n%s", m.ID, got, want, rec.Body.String())
		}
		var ls []string
		for _, l := range m.Levels {
			ls = append(ls, l.Effort)
		}
		if got, want := strings.Join(ls, ","), "low,high,max"; got != want {
			t.Fatalf("%s: supported_reasoning_levels %q, want %q", m.ID, got, want)
		}
		return
	}
	t.Fatalf("no digua/deepseek-flash\n%s", rec.Body.String())
}
