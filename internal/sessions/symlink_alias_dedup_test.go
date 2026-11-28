package sessions

import (
	"os"
	"path/filepath"
	"testing"
)

// A link inside projects/ that points at another project folder in the same
// projects/ is the same folder under a second name. Sessions must count the
// transcripts once (#6578): ccFiles feeds the Sessions page, and a second
// name for one folder currently doubles its output tokens and cache reads.
func TestProjectFolderAliasNotCountedTwice(t *testing.T) {
	claude, _ := setup(t)
	projects := filepath.Join(claude, "projects")

	before := ccFiles("claude", claude)
	real := filepath.Join(projects, "-work-app")
	if _, err := os.Stat(real); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(projects, "-work-app-alias")
	linkFolder(t, real, alias)

	after := ccFiles("claude", claude)
	if len(after) != len(before) {
		t.Fatalf("a second name for one project folder doubled sessions: %d files, want %d", len(after), len(before))
	}
}
