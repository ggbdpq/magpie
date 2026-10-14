package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// linkThrough moves the folder at link to real and leaves a link in its
// place, the way a folder on another drive is reached: a symlink elsewhere,
// a junction on Windows (#2233). Skips where a link cannot be made.
func linkThrough(t *testing.T, link, real string) {
	t.Helper()
	if err := os.Rename(link, real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		os.Rename(real, link)
		t.Skipf("no link: %v", err)
	}
}

// A WorkBuddy folder whose projects folder is a link lists its sessions as
// they are (#2233's shape, on WorkBuddy's own folder), and CodeBuddy's are
// read through its link too.
func TestBuddyFilesThroughALinkedProjectsFolder(t *testing.T) {
	claude, _ := setup(t)
	home := filepath.Join(filepath.Dir(claude), "home")
	outside := filepath.Dir(claude)

	wb := filepath.Join(home, ".workbuddy")
	copyTree(t, "testdata/workbuddy", wb)
	wbReal := filepath.Join(outside, "workbuddy-projects")
	linkThrough(t, filepath.Join(wb, "projects"), wbReal)
	wbReal, _ = filepath.EvalSymlinks(wbReal) // /var is /private/var on macOS
	fs := workbuddyFiles()
	if len(fs) != 3 {
		t.Fatalf("want WorkBuddy's 3 session files behind the link, got %d: %+v", len(fs), fs)
	}
	for _, f := range fs {
		if f.agent != "workbuddy" || !f.main || !strings.HasPrefix(f.path, wbReal) {
			t.Fatalf("workbuddy session behind the link: %+v", f)
		}
	}

	cb := filepath.Join(home, ".codebuddy")
	copyTree(t, "testdata/codebuddy", cb)
	cbReal := filepath.Join(outside, "codebuddy-projects")
	linkThrough(t, filepath.Join(cb, "projects"), cbReal)
	cbReal, _ = filepath.EvalSymlinks(cbReal)
	cfs := buddyFiles("codebuddy", CodeBuddyDir())
	if len(cfs) != 2 {
		t.Fatalf("want CodeBuddy's 2 session files behind the link, got %d: %+v", len(cfs), cfs)
	}
	for _, f := range cfs {
		if f.agent != "codebuddy" || !strings.HasPrefix(f.path, cbReal) {
			t.Fatalf("codebuddy session behind the link: %+v", f)
		}
	}
}

// MiniMax Code's calls are read when its v2/sessions folder is a link
// (#2233's shape, on MiniMax's own folder).
func TestMiniMaxCallsThroughALinkedSessionsFolder(t *testing.T) {
	claude, _ := setup(t)
	dir := filepath.Join(filepath.Dir(claude), "elsewhere")
	t.Setenv("MINIMAX_DATA_DIR", dir)
	copyTree(t, "testdata/minimax", dir)
	mmReal := filepath.Join(dir, "v2", "sessions-elsewhere")
	linkThrough(t, filepath.Join(dir, "v2", "sessions"), mmReal)
	var got []Call
	for _, c := range Calls(time.Time{}) {
		if c.Agent == "minimax-code" {
			got = append(got, c)
		}
	}
	if len(got) != 1 {
		t.Fatalf("want MiniMax Code's one call on its own relay behind the link, got %d: %+v", len(got), got)
	}
	if c := got[0]; c.Model != "m2" || c.Upstream != "custom_provider:relay" {
		t.Fatalf("model %q upstream %q", c.Model, c.Upstream)
	}
}
