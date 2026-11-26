package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// TestCachedPreservesLastKnownGoodDuringReload (#7863): while a plugin-host
// reload is in flight (triggered by checkList) and plugin-providers.json is
// missing, unreadable, or incomplete, Cached must keep serving the previous
// last-known-good models for enabled plugins until the replacement host
// commits its fresh list, while still dropping disabled/removed plugins and
// picking up providers added by another magpie.
func TestCachedPreservesLastKnownGoodDuringReload(t *testing.T) {
	storeSandbox(t)
	every := pathsEvery
	pathsEvery = 0
	t.Cleanup(func() { pathsEvery = every })

	dir := filepath.Join(t.TempDir(), "my-provider")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(dir, "index.js")
	base := time.Now().Add(-time.Hour)
	if err := os.WriteFile(entry, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(entry, base, base); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(List{Plugins: []Entry{{Spec: dir}}})
	writeList(t, string(b))

	// Block background refreshes so we deterministically inspect the reload window.
	block := make(chan struct{})
	defer close(block)
	oldRefresh := refreshProviders
	refreshProviders = func(ctx context.Context, epoch uint64) error {
		select {
		case <-block:
		case <-ctx.Done():
		}
		return context.Canceled
	}
	t.Cleanup(func() {
		refreshProviders = oldRefresh
		forgetProviders()
		Refreshed()
	})

	// Seed a last-known-good provider snapshot in memory and remove the disk
	// file to simulate a reload window where disk has no readable snapshot.
	UseCached([]Provider{{
		ID:     "acme",
		Spec:   dir,
		Name:   "Acme",
		Models: []Model{{ID: "acme-1", Name: "Acme 1"}},
	}})
	_ = os.Remove(providersPath())

	// Trigger a path-plugin hot-reload via checkList().
	if err := os.WriteFile(entry, []byte("v2-reloading"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(entry, base.Add(time.Minute), base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	got := Cached()
	if !slices.ContainsFunc(got, func(p Provider) bool {
		return p.ID == "acme" && len(p.Models) == 1 && p.Models[0].ID == "acme-1"
	}) {
		t.Fatalf("Cached() dropped last-known-good plugin models during reload: %+v", got)
	}

	// If another magpie wrote a partial plugin-providers.json containing a new
	// provider without acme, Cached() must surface the new provider AND retain acme.
	otherDir := filepath.Join(t.TempDir(), "other-provider")
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatal(err)
	}
	savedBytes, _ := json.Marshal([]Provider{{
		ID:     "beta",
		Spec:   otherDir,
		Name:   "Beta",
		Models: []Model{{ID: "beta-1", Name: "Beta 1"}},
	}})
	if err := os.WriteFile(providersPath(), savedBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(List{Plugins: []Entry{{Spec: dir}, {Spec: otherDir}}})
	writeList(t, string(b))
	if err := os.Chtimes(listPath(), base.Add(2*time.Minute), base.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got = Cached()
	if !slices.ContainsFunc(got, func(p Provider) bool { return p.ID == "acme" && len(p.Models) == 1 }) ||
		!slices.ContainsFunc(got, func(p Provider) bool { return p.ID == "beta" && len(p.Models) == 1 }) {
		t.Fatalf("Cached() after partial disk snapshot = %+v, want both acme and beta", got)
	}

	// Disabling acme in plugins.json must drop it immediately even with provCache preserved.
	b, _ = json.Marshal(List{Plugins: []Entry{{Spec: dir, Off: true}, {Spec: otherDir}}})
	writeList(t, string(b))
	if err := os.Chtimes(listPath(), base.Add(3*time.Minute), base.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got = Cached()
	if slices.ContainsFunc(got, func(p Provider) bool { return p.ID == "acme" }) {
		t.Fatalf("disabled plugin acme still returned by Cached(): %+v", got)
	}
	if !slices.ContainsFunc(got, func(p Provider) bool { return p.ID == "beta" }) {
		t.Fatalf("enabled plugin beta missing from Cached(): %+v", got)
	}
}
