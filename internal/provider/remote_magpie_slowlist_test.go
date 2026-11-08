package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agentenv"
)

// The extended catalog a remote magpie answers the X-Magpie-* headers with
// is a far bigger body than its plain list: over a slow link the transfer
// is cut before it ends, every time. The plain list — asked without those
// headers — comes through, and a remote whose catalog can't arrive whole
// is still added by it: the models, without the prices, drawers and
// decision models, which the next whole fetch brings back (#5457).
func TestRemoteMagpieCutCatalogFallsBackToPlainList(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}

	whole := `{"object":"list","data":[` + strings.Repeat(`{"id":"p/big"},`, 500) + `{"id":"p/last"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Magpie-Deciders") != "" {
			// the extended catalog: declared whole, sent cut, closed
			rw.Header().Set("Content-Type", "application/json")
			rw.Header().Set("Content-Length", strconv.Itoa(len(whole)))
			rw.Write([]byte(whole[:4096]))
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		rw.Write([]byte(`{"object":"list","data":[{"id":"p/gpt-6"},{"id":"p/qwen3.6-max"}]}`))
	}))
	defer srv.Close()

	if err := Save(Provider{ID: "remote", Name: "Remote", Preset: RemoteMagpiePreset, Chat: srv.URL, Key: "sk-remote"}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("remote")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatalf("a remote whose catalog is cut is added by its plain list: %v", err)
	}
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	if len(ids) != 2 {
		t.Fatalf("the plain list's models: %v", ids)
	}
}

// When the plain list is cut too, every endpoint's fetch fails, and what
// the user is told is that the bodies were cut, not that their shape was
// wrong (#5457).
func TestRemoteMagpieCutEverywhereSaysIncomplete(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}

	whole := `{"object":"list","data":[{"id":"p/gpt-6"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		rw.Header().Set("Content-Length", strconv.Itoa(len(whole)))
		rw.Write([]byte(whole[:10]))
	}))
	defer srv.Close()

	if err := Save(Provider{ID: "remote", Name: "Remote", Preset: RemoteMagpiePreset, Chat: srv.URL, Key: "sk-remote"}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("remote")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(context.Background()); err == nil {
		t.Fatal("a remote whose every list is cut was taken for fetched")
	} else if !strings.Contains(err.Error(), "incomplete") || strings.Contains(err.Error(), "not a model list") {
		t.Fatalf("the cut is not told from the shape: %v", err)
	}
}
