package gui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A provider whose vendor list names image models says which, for its
// editor to list apart from the models agents chat with.
func TestProviderInfoListsImageModels(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"id":"gpt-5.5"},{"id":"gpt-image-2"},{"id":"flux-kontext-pro"}]}`)
	}))
	defer up.Close()
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: up.URL + "/v1", Key: "key"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(t.Context()); err != nil {
		t.Fatal(err)
	}
	j := providerInfo(p, nil)
	if got := strings.Join(j.DrawIDs, ","); got != "gpt-image-2,flux-kontext-pro" || j.Draws != 2 {
		t.Fatalf("drawIds %q draws %d", got, j.Draws)
	}
	for _, m := range j.Models {
		if m.ID != "gpt-5.5" {
			t.Fatalf("image model %s among the chat ones", m.ID)
		}
	}
}

// A model the user said makes videos or images (SetModelKind, #1399) leaves
// the chat list (#2071), the editor lists the video ones apart, and either
// area carries the marked model's kind, for its chip to say it again by
// right-click.
func TestProviderInfoCarriesUserKinds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"id":"claude-sonnet-4-5"},{"id":"omni_flash-10s"}]}`)
	}))
	defer up.Close()
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: up.URL + "/v1", Key: "key"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelKind("relay/omni_flash-10s", provider.KindVideo); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelKind("relay/claude-sonnet-4-5", provider.KindImage); err != nil {
		t.Fatal(err)
	}
	j := providerInfo(p, nil)
	for _, m := range j.Models {
		if m.ID == "omni_flash-10s" || m.ID == "claude-sonnet-4-5" {
			t.Fatalf("a marked model among the chat ones: %s", m.ID)
		}
	}
	if got := strings.Join(j.VideoIDs, ","); got != "omni_flash-10s" {
		t.Fatalf("videoIds %q", got)
	}
	if !slices.Contains(j.DrawIDs, "claude-sonnet-4-5") {
		t.Fatalf("drawIds %v lost the one said to draw", j.DrawIDs)
	}
	if j.Kinds["omni_flash-10s"] != provider.KindVideo || j.Kinds["claude-sonnet-4-5"] != provider.KindImage {
		t.Fatalf("kinds %v", j.Kinds)
	}
}
