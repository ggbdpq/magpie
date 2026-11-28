package library

import (
	"os"
	"path/filepath"
	"testing"
)

// A skill the library keeps from a folder source, for a readsShared agent
// (Droid), must not be left twice in that agent's reading paths: a stale
// non-magpie link in the agent's own skill folder that points straight at
// the same real folder as the library's skill is set aside on sync, so the
// agent reads one skill rather than warning about a duplicate (#6425).
//
// The reported shape: ~/.agents/skills/<n> and the library's
// ~/.config/magpie/library/skills/<n> both point at an external folder, and
// ~/.factory/skills/<n> is a third, stale link straight at that folder (not
// at the library's link). Droid scans its own and the shared folder at the
// same level, without realpath dedup, so it loads the skill twice.
func TestSharedStaleLinkToLibraryTargetSetAside(t *testing.T) {
	h := sandbox(t)
	write(t, filepath.Join(h, ".factory/settings.json"), "")
	ext := filepath.Join(h, "external/ego-skills")
	skill(t, ext, "ego-browser", "Ego browser")

	// the library skill from a folder source pointing at ext
	ok(t)(InstallSkills(ext, []string{""}, []string{"droid"}))
	lib := skillDir("ego-browser")
	shared := filepath.Join(h, ".agents/skills/ego-browser")
	own := filepath.Join(h, ".factory/skills/ego-browser")
	if _, err := os.Lstat(shared); err != nil {
		// Droid reads the shared folder; magpie keeps the skill there
		if err := os.MkdirAll(filepath.Dir(shared), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(ext, shared); err != nil {
			t.Fatal(err)
		}
	}
	// stale non-magpie link in the agent's own folder, straight at ext:
	// replace whatever the install put there with the user's old link
	if err := os.MkdirAll(filepath.Dir(own), 0o755); err != nil {
		t.Fatal(err)
	}
	os.Remove(own)
	if err := os.Symlink(ext, own); err != nil {
		t.Fatal(err)
	}
	if !linked(own) || realDir(own) != realDir(lib) {
		t.Fatalf("test setup: own link %q does not resolve to the library target", own)
	}

	ok(t)(Sync())

	if _, err := os.Lstat(own); !os.IsNotExist(err) {
		t.Fatalf("the stale link %q is still in Droid's own skill folder (err %v): Droid loads the skill from both roots", own, err)
	}
	// the skill itself, reached through the shared folder Droid reads, is
	// untouched: only the duplicate link in Droid's own folder went
	if got := read(t, filepath.Join(ext, "SKILL.md")); got == "" {
		t.Error("the library's skill content went missing")
	}
}
