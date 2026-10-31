package library

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/agent"
)

// wslLinkSeams stands in, for a test, for the distro's answers and for
// where its paths open: readlink says what each distro is asked of each
// path, r is where each distro's / is (the share), win where its drives
// are; a path or drive nobody said anything of is an error to the test.
func wslLinkSeams(t *testing.T, readlink, r, win map[string]string) {
	t.Helper()
	oldReadlink, oldShare, oldDrive := wslReadlink, wslSharePath, wslDrivePath
	wslReadlink = func(distro, path string) (string, error) {
		out, ok := readlink[distro+"\x00"+path]
		if !ok {
			return "", errors.New("unexpected ask: readlink -f " + path + " in " + distro)
		}
		return out, nil
	}
	wslSharePath = func(distro, linux string) string {
		if root, ok := r[distro]; ok {
			return filepath.Join(root, filepath.FromSlash(linux))
		}
		t.Errorf("unexpected share path %s of %s", linux, distro)
		return oldShare(distro, linux)
	}
	wslDrivePath = func(drive, rest string) string {
		if root, ok := win[drive]; ok {
			return filepath.Join(root, filepath.FromSlash(rest))
		}
		t.Errorf("unexpected drive path /mnt/%s%s", drive, rest)
		return oldDrive(drive, rest)
	}
	t.Cleanup(func() { wslReadlink, wslSharePath, wslDrivePath = oldReadlink, oldShare, oldDrive })
}

// Where a resolved path of the distro opens: a drive's own folder under
// /mnt/<drive>/, the share anywhere else — and /mnt/tools is a folder
// called tools, not a drive called t (#4502).
func TestWSLResolvedPath(t *testing.T) {
	oldShare, oldDrive := wslSharePath, wslDrivePath
	wslSharePath = func(distro, linux string) string { return "share:" + distro + ":" + linux }
	wslDrivePath = func(drive, rest string) string { return "drive:" + drive + ":" + rest }
	t.Cleanup(func() { wslSharePath, wslDrivePath = oldShare, oldDrive })
	for in, want := range map[[2]string]string{
		{"Ubuntu-24.04", "/mnt/c/Users/me/.codex"}: "drive:c:/Users/me/.codex",
		{"Ubuntu-24.04", "/mnt/d/x"}:               "drive:d:/x",
		{"Ubuntu-24.04", "/home/me/.codex"}:        "share:Ubuntu-24.04:/home/me/.codex",
		{"Ubuntu", "/root"}:                        "share:Ubuntu:/root",
		{"Ubuntu-24.04", "/mnt/tools/x"}:           "share:Ubuntu-24.04:/mnt/tools/x",
		{"Ubuntu-24.04", "/mnt/wsl/x"}:             "share:Ubuntu-24.04:/mnt/wsl/x",
		{"Ubuntu-24.04", "/mnt/c"}:                 "share:Ubuntu-24.04:/mnt/c",
	} {
		if got := wslResolvedPath(in[0], in[1]); got != want {
			t.Errorf("wslResolvedPath(%s, %s) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

// wslLink leaves alone everything the share tells of itself: a folder
// that follows, one that isn't there yet, a file in the way (mkdir's own
// "not a directory" is the word, not a link to resolve), an agent whose
// distro has no place here, and a link the distro cannot account for —
// what mkdir then says of p as it is, is the error to keep (#4502).
func TestWSLLinkLeavesWhatTheShareSaysAlone(t *testing.T) {
	root := t.TempDir()
	h := filepath.Join(root, "home", "me")
	if err := os.MkdirAll(h, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(h, ".codex", "skills", "pdf")
	a := &agent.Agent{WSL: "Ubuntu-24.04", Home: h, HomeLinux: "/home/me"}

	// a folder that follows is no link to resolve
	if err := os.MkdirAll(filepath.Join(h, ".codex", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := wslLink(a, p); got != p {
		t.Errorf("a folder that follows: %q", got)
	}

	// a file in the way is no link: mkdir's own word stands
	if err := os.RemoveAll(filepath.Join(h, ".codex")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h, ".codex"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := wslLink(a, p); got != p {
		t.Errorf("a file in the way: %q", got)
	}
	if err := os.MkdirAll(filepath.Join(h, ".codex", "skills"), 0o755); err == nil {
		t.Fatal("mkdir over a file: wanted an error")
	}

	// a link to nothing is what the distro is asked of
	if err := os.Remove(filepath.Join(h, ".codex")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(h, "elsewhere"), filepath.Join(h, ".codex")); err != nil {
		t.Fatal(err)
	}
	old := wslReadlink
	t.Cleanup(func() { wslReadlink = old })

	// the distro cannot be asked: p as it is, and the error to come is
	// mkdir's, kept
	wslReadlink = func(distro, path string) (string, error) { return "", errors.New("no answer") }
	if got := wslLink(a, p); got != p {
		t.Errorf("no answer from the distro: %q", got)
	}

	// an answer that says nothing, or the link itself: p as it is
	wslReadlink = func(distro, path string) (string, error) { return "", nil }
	if got := wslLink(a, p); got != p {
		t.Errorf("an empty answer: %q", got)
	}
	wslReadlink = func(distro, path string) (string, error) { return path, nil }
	if got := wslLink(a, p); got != p {
		t.Errorf("an answer of the link itself: %q", got)
	}

	// an agent whose files are none of a distro's is out of it
	if got := wslLink(&agent.Agent{}, p); got != p {
		t.Errorf("no distro: %q", got)
	}
}
