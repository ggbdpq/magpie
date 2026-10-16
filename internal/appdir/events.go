package appdir

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Event appends a line to events.log in magpie's config folder: the
// management actions (an agent connected, disconnected, reapplied) go in
// with the time and where they came from, so the one nobody saw can still
// be placed later (#2545: Codex was found disconnected, and neither its
// files nor the gateway said what ran Disconnect — the gateway keeps no
// log of its /api requests, and a GUI app's stdout goes nowhere). A failed
// line is never worth failing the action over.
func Event(what string) {
	os.MkdirAll(Config(), 0o700)
	f, err := os.OpenFile(filepath.Join(Config(), "events.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), what)
}
