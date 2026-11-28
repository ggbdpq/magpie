package library

import (
	"path/filepath"
	"strings"
	"testing"
)

// A Pi prompt's argument-hint is metadata the user set. When the library
// is carried to another computer, the generated Pi prompt must keep the
// hint, just as it keeps the description (HenryQW, #6831).
func TestCommandArgumentHintCarries(t *testing.T) {
	h := sandbox(t)
	write(t, filepath.Join(h, ".pi/agent/prompts/focus.md"),
		"---\ndescription: Review focus\nargument-hint: \"[focus]\"\n---\nReview $ARGUMENTS\n")

	p := probeCommands(t)
	paths := candidatePaths(p)
	if len(paths) == 0 {
		t.Fatal("no candidate found")
	}
	ok(t)(ImportCommands(paths))
	b := must(Collect())

	// other computer: a fresh home with no Pi prompt of our own
	h2 := sandbox(t)
	write(t, filepath.Join(h2, ".pi/agent/settings.json"), "")
	ok(t)(Put(b))

	got := read(t, filepath.Join(h2, ".pi/agent/prompts/focus.md"))
	if !strings.Contains(got, `argument-hint: "[focus]"`) {
		t.Errorf("generated Pi prompt lost argument-hint; got:\n%s\nwant a line with argument-hint: \"[focus]\"", got)
	}
}
