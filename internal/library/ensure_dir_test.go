package library

import (
	"os"
	"path/filepath"
	"testing"
)

// ensureDir takes a mkdir's "already there" where MkdirAll gives up
// (#4502): a Codex in a WSL distro may keep its ~/.codex as a symlink to
// the user's Windows folder, and the \\wsl.localhost view of the distro
// answers a mkdir of the name with EEXIST while stat and lstat can't say
// what it is — Go's MkdirAll forgives EEXIST only when lstat says folder.
// Here, where links are seen through, the name a mkdir calls already
// there while no stat calls a folder is a link to nothing: what MkdirAll
// reports, and ensureDir takes, since the writes that follow tell.
func TestEnsureDirTakesUnseeableExistence(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, ".codex")
	if err := os.Symlink(filepath.Join(root, "elsewhere"), p); err != nil {
		t.Fatal(err)
	}
	if err := ensureDir(p); err != nil {
		t.Fatalf("ensureDir(%s): %v", p, err)
	}
}

// The folder the link points at is the folder to make a folder of, as it
// always was: a skills folder under the link lands behind it, not over it
// (#4502).
func TestEnsureDirMakesWayThroughALink(t *testing.T) {
	root := t.TempDir()
	elsewhere := filepath.Join(root, "elsewhere")
	if err := os.Mkdir(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, ".codex")
	if err := os.Symlink(elsewhere, p); err != nil {
		t.Fatal(err)
	}
	skills := filepath.Join(p, "skills")
	if err := ensureDir(skills); err != nil {
		t.Fatalf("ensureDir(%s): %v", skills, err)
	}
	if fi, err := os.Lstat(filepath.Join(elsewhere, "skills")); err != nil || !fi.IsDir() {
		t.Fatalf("skills not in the folder the link points at: %v", err)
	}
}

// A file in the way is no folder and says so: EEXIST aside, and where the
// file can be seen, MkdirAll's own word (not a directory) stands.
func TestEnsureDirKeepsAFileInTheWayAnError(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, ".codex")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureDir(p); err == nil {
		t.Fatal("ensureDir over a file: wanted an error")
	}
	if err := ensureDir(filepath.Join(p, "skills")); err == nil {
		t.Fatal("ensureDir under a file: wanted an error")
	}
}
