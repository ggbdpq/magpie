package plugin

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeInfo is what a junction component looks like to Lstat on Windows: a
// reparse point that, since Go 1.23, is neither ModeSymlink nor ModeDir
// (#4265).
type fakeInfo struct {
	name string
	mode os.FileMode
}

func (f fakeInfo) Name() string       { return f.name }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() os.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.mode&os.ModeDir != 0 }
func (f fakeInfo) Sys() any           { return nil }

// Scoop and the like hang magpie's data folder off the install folder
// through a link (an NTFS junction): a config dir through the link holds
// the very files the real one does, so its pages register all the same
// (#4265).
func TestWindowPageThroughLinkedConfigDir(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(root, "real")
	pkg := filepath.Join(real, "plugins", "node_modules", "github.com", "owner", "chat")
	if err := os.MkdirAll(filepath.Join(pkg, "page"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"chat","main":"host.js","magpie":{"page":{"title":"Chat","file":"page/index.html"},"permissions":["usage:summary"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "page", "index.html"), []byte("<p>hi</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "data")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(link, "plugins", "node_modules", "github.com", "owner", "chat")
	w, only := Window(target)
	if w == nil || w.Page == nil || w.Page.Title != "Chat" || only {
		t.Fatalf("Window through a linked config dir = %+v only=%v, want the page", w, only)
	}
	if f, ok := FileIn(w.Page.Dir, "index.html"); !ok || filepath.Base(f) != "index.html" {
		t.Errorf("FileIn through the linked config dir = %q %v", f, ok)
	}
}

// A junction on Windows is a reparse point that Go 1.23's Lstat marks
// neither ModeSymlink nor ModeDir, so filepath.EvalSymlinks gives up on
// any path that crosses one with ENOTDIR and the page check turned every
// junction-borne config dir away (#4265). inside's resolving follows the
// junction as well: a page in the dir comes in, a junction out of the dir
// still stays out, a dead link stays out (fail closed), and a reparse
// point whose target can't be read (a OneDrive placeholder, say) counts
// as the plain folder it serves.
func TestInsideFollowsJunctions(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(root, "store")
	pkg := filepath.Join(store, "pkg")
	if err := os.MkdirAll(filepath.Join(pkg, "page"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "page", "index.html"), []byte("<p>hi</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(pkg, "onedrive"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "onedrive", "index.html"), []byte("<p>hi</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secret.html"), []byte("<p>no</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "junction")
	if err := os.Symlink(store, link); err != nil {
		t.Fatal(err)
	}
	// out.html leads out of the folder, dead.html leads nowhere: both
	// stand in for junctions of the kind
	out := filepath.Join(pkg, "out.html")
	if err := os.Symlink(filepath.Join(root, "secret.html"), out); err != nil {
		t.Fatal(err)
	}
	dead := filepath.Join(pkg, "page", "dead.html")
	if err := os.Symlink(filepath.Join(root, "gone.html"), dead); err != nil {
		t.Fatal(err)
	}

	// the junctions: Lstat marks them a bare reparse point, as Windows
	// does since Go 1.23, and the placeholder reads no target
	realLstat, realReadlink := lstat, readlink
	lstat = func(p string) (os.FileInfo, error) {
		fi, err := realLstat(p)
		if err != nil {
			return nil, err
		}
		if p == link || p == out || strings.HasPrefix(p, link+string(filepath.Separator)) || p == filepath.Join(pkg, "onedrive") {
			return fakeInfo{name: fi.Name(), mode: fi.Mode() & ^(os.ModeSymlink|os.ModeDir) | os.ModeIrregular}, nil
		}
		return fi, nil
	}
	readlink = func(p string) (string, error) {
		if p == filepath.Join(pkg, "onedrive") {
			return "", errors.New("a placeholder reads no target")
		}
		return realReadlink(p)
	}
	t.Cleanup(func() { lstat, readlink = realLstat, realReadlink })

	dir := filepath.Join(link, "pkg")
	if file, ok := htmlInside(dir, "page/index.html"); !ok || filepath.Base(file) != "index.html" {
		t.Errorf("a page in a junction-borne config dir: %q %v", file, ok)
	}
	if f, ok := FileIn(dir, "page/index.html"); !ok || filepath.Base(f) != "index.html" {
		t.Errorf("FileIn in a junction-borne config dir: %q %v", f, ok)
	}
	if file, ok := htmlInside(dir, "out.html"); ok {
		t.Errorf("a junction out of the folder served %q", file)
	}
	if file, ok := htmlInside(dir, "../secret.html"); ok {
		t.Errorf(".. out of the folder served %q", file)
	}
	if file, ok := htmlInside(dir, "page/dead.html"); ok {
		t.Errorf("a dead link served %q", file)
	}
	if file, ok := htmlInside(dir, "onedrive/index.html"); !ok || filepath.Base(file) != "index.html" {
		t.Errorf("a placeholder without a target counted against its folder: %q %v", file, ok)
	}
}
