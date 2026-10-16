package appdir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Events go into events.log in magpie's config folder, one line each, the
// time first: readable back with nothing else set up.
func TestEventNotesWhoDidWhat(t *testing.T) {
	Event("api disconnect codex from 127.0.0.1:5001 (webview/1.2.3)")
	Event("cli codex default")
	b, err := os.ReadFile(filepath.Join(Config(), "events.log"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("events.log holds %d lines:\n%s", len(lines), b)
	}
	for i, want := range []string{"api disconnect codex from 127.0.0.1:5001 (webview/1.2.3)", "cli codex default"} {
		stamp, rest, _ := strings.Cut(lines[i], " ")
		when, err := time.Parse(time.RFC3339, stamp)
		if err != nil {
			t.Fatalf("line %d does not start with a time: %q", i, lines[i])
		}
		if d := time.Since(when); d > time.Minute || d < -time.Minute {
			t.Fatalf("line %d is not of just now: %v", i, d)
		}
		if rest != want {
			t.Fatalf("line %d says %q, want %q", i, rest, want)
		}
	}
}
