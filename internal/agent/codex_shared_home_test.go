package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
)

// OpenResearch shares config.toml with the desktop app, but runs Codex
// with its own CODEX_HOME. Codex resolves a relative model_catalog_json
// from that home, where magpie's catalogs were never written.
func TestCodexCatalogAcrossHomes(t *testing.T) {
	for _, desktop := range []bool{false, true} {
		name, auth := "provider", ""
		if desktop {
			name, auth = "desktop", chatGPTAuth
		}
		t.Run(name, func(t *testing.T) {
			home, _ := codexHome(t, auth, "model = \"gpt-5.5\"\n")
			app := true
			codexAppReads(t, &app)
			list := `{"models":[{"slug":"gpt-6-sol"},{"slug":"fake/m1"}]}`
			deskListIs(t, &list)
			cx := codex(home)
			connect := func() {
				t.Helper()
				if desktop {
					if ok, err := cx.Join(); err != nil || !ok {
						t.Fatalf("join: %v %v", ok, err)
					}
				} else if err := cx.Fields[0].Set("fake/m1"); err != nil {
					t.Fatal(err)
				}
			}
			connect()
			private := filepath.Join(home, "OpenResearch", "agents", "codex")
			if err := os.MkdirAll(private, 0o755); err != nil {
				t.Fatal(err)
			}
			// Copy the shared config's bytes, so this guarantee is checked
			// even on Windows without permission to create symlinks.
			check := func() string {
				t.Helper()
				b, err := os.ReadFile(cx.Path)
				if err != nil {
					t.Fatal(err)
				}
				cfg := filepath.Join(private, "config.toml")
				if err := os.WriteFile(cfg, b, 0o644); err != nil {
					t.Fatal(err)
				}
				ref, ok := edit.GetTOMLTop(cfg, "model_catalog_json")
				if !ok || ref == "" {
					t.Fatalf("shared config has no catalog: %q", ref)
				}
				resolved := ref
				if !filepath.IsAbs(resolved) {
					resolved = filepath.Join(private, resolved)
				}
				got, err := os.ReadFile(resolved)
				if err != nil {
					t.Fatalf("catalog %q cannot be read from a private CODEX_HOME: %v", ref, err)
				}
				want, err := os.ReadFile(filepath.Join(home, ".codex", filepath.Base(resolved)))
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("private CODEX_HOME did not read magpie's current catalog: %v", err)
				}
				return resolved
			}
			catalog := check()
			list = `{"models":[{"slug":"gpt-6-sol"},{"slug":"fake/m2"}]}`
			if err := cx.Sync(); err != nil {
				t.Fatal(err)
			}
			check()
			// TOML writers commonly use forward slashes on Windows. The
			// same absolute path is still magpie's, not a user's catalog.
			if err := edit.SetTOMLTop(cx.Path, edit.KV{Path: "model_catalog_json", Value: filepath.ToSlash(catalog)}); err != nil {
				t.Fatal(err)
			}
			cx = codex(home)
			if err := cx.Sync(); err != nil {
				t.Fatal(err)
			}
			check()
			// Existing relative paths may be needed by a WSL reader. A
			// sync or another connect keeps them; disconnect still removes
			// the key and file, and the next connection uses the new default.
			relative := "./" + filepath.Base(catalog)
			if err := edit.SetTOMLTop(cx.Path, edit.KV{Path: "model_catalog_json", Value: relative}); err != nil {
				t.Fatal(err)
			}
			cx = codex(home)
			if err := cx.Sync(); err != nil {
				t.Fatal(err)
			}
			connect()
			if ref, _ := edit.GetTOMLTop(cx.Path, "model_catalog_json"); ref != relative {
				t.Fatalf("existing relative catalog changed: %q", ref)
			}
			if msg := cx.Check(); msg != "" {
				t.Fatalf("relative catalog not recognized: %s", msg)
			}
			if err := cx.Disconnect(); err != nil {
				t.Fatal(err)
			}
			if ref, _ := edit.GetTOMLTop(cx.Path, "model_catalog_json"); ref != "" {
				t.Fatalf("disconnect left a catalog reference: %q", ref)
			}
			if _, err := os.Stat(catalog); !os.IsNotExist(err) {
				t.Fatalf("disconnect left magpie's catalog: %v", err)
			}
			connect()
			check()
		})
	}
}
