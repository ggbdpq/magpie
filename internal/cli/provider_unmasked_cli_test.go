package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The user's own model at their own computer adds itself with the masking
// switch on (provider.UnmaskedOnAdd, the owner 11-18): `magpie provider add
// mybox chat=http://127.0.0.1:8080/v1`, and a local preset at the address it
// is given. `unmasked=no` is their word for it and stands, whatever the
// address says; a vendor's provider is added masked, as before.
func TestAddProviderUnmaskedForALocalServer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	box := localModels(t)
	if err := addProvider([]string{"mybox", "chat=" + box.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("mybox")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Unredacted {
		t.Fatalf("a model on this computer was added masked: %+v", p)
	}
	if p.Chat != box.URL+"/v1" {
		t.Fatalf("chat %q, want %s/v1", p.Chat, box.URL)
	}

	// a local preset, moved to the server by the address it is added with
	studio := localModels(t)
	if err := addProvider([]string{"lmstudio", "address=" + strings.TrimPrefix(studio.URL, "http://")}); err != nil {
		t.Fatal(err)
	}
	if p, err = provider.Find("lmstudio"); err != nil {
		t.Fatal(err)
	}
	if !p.Unredacted {
		t.Fatal("a local preset at the address it was added with came in masked")
	}

	// the switch said in the command is the user's, and nothing reads the
	// address over it
	quiet := localModels(t)
	if err := addProvider([]string{"quiet box", "chat=" + quiet.URL + "/v1", "unmasked=no"}); err != nil {
		t.Fatal(err)
	}
	if p, err = provider.Find("quiet-box"); err != nil {
		t.Fatal(err)
	}
	if p.Unredacted {
		t.Fatal("unmasked=no was overridden by the address")
	}
}

// localModels is a local server answering a model list, as one on this
// computer does.
func localModels(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"qwen3.5-9b","object":"model","owned_by":"organization_owner"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}
