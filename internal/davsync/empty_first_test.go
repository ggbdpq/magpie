package davsync

import (
	"context"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A computer with no setup of its own that joins first must not let its
// empty file clear out what a computer joining after it has: an empty
// part on the server is the folder never having had one, not a removal.
// Three computers picking the library alone (#5642): whichever empty one
// writes first, the one with providers and routing groups keeps them,
// and its setup is what the others get.
func TestSyncEmptyFirstKeepsLaterSetups(t *testing.T) {
	fake := &fakeDAV{files: map[string][]byte{}, etags: map[string]string{}, dirs: map[string]bool{"/dav": true}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ctx := context.Background()
	lib := true // the library alone, as the reporter had it
	cfg := Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse", Library: &lib}
	now := func(t *testing.T) {
		t.Helper()
		if err := Now(ctx); err != nil {
			t.Fatal(err)
		}
	}

	// b joins first, with nothing of its own: its empty setup goes up
	b := newComputer(t)
	b.use(t)
	Configure(cfg)
	now(t)

	// a joins after it, with providers and a routing group: none of it
	// goes away
	a := newComputer(t)
	a.use(t)
	provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k1"})
	if err := provider.SaveGroup(provider.Group{ID: "fast", Name: "Fast", Members: []string{"deepseek/deepseek-chat"}}); err != nil {
		t.Fatal(err)
	}
	Configure(cfg)
	now(t)
	if got := ids(); !slices.Equal(got, []string{"deepseek=k1"}) {
		t.Fatalf("a's providers after joining: %v", got)
	}
	gs, err := provider.StoredGroups()
	if err != nil || len(gs) != 1 || gs[0].ID != "fast" {
		t.Fatalf("a's groups after joining: %v %v", gs, err)
	}

	// what a has goes up: b, syncing next, has it too (without the key,
	// which stays a's), and a keeps it when b's sync comes back
	b.use(t)
	now(t)
	if got := ids(); !slices.Equal(got, []string{"deepseek="}) {
		t.Fatalf("b after a joined: %v", got)
	}
	a.use(t)
	now(t)
	if got := ids(); !slices.Equal(got, []string{"deepseek=k1"}) {
		t.Fatalf("a after b's sync came back: %v", got)
	}
	gs, err = provider.StoredGroups()
	if err != nil || len(gs) != 1 || gs[0].ID != "fast" {
		t.Fatalf("a's groups after b's sync came back: %v %v", gs, err)
	}
}
