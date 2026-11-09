package agent

// dsh's desktop rewrites its own cordis.patch.yml in place — truncate, then
// the body — so for a moment the file is its head alone: magpie's route
// entries sit up front, and a round that read in that window saw a wired
// list of two and wrote that half-state back as the whole list. The user's
// other entries were gone for good, over and over (#5782, stevenpisces: 57
// entries became 2). A fresh list is reread after a beat; one that moved
// under us is left to the next round, which reads it whole.

import (
	"os"
	"strings"
	"testing"
	"time"
)

// dshHalfPair builds the desktop's list and its mid-write head: the route
// entries magpie wrote first, the user's own body after.
func dshHalfPair() (whole, half string) {
	var b strings.Builder
	b.WriteString("# Your patch layer for this dsh profile.\n")
	b.WriteString(`- id: llm-pi-ai # magpie
  config:
    providers:
      magpie:
        type: api
        baseURL: http://127.0.0.1:1
        models:
          - id: m1
`)
	b.WriteString(`- id: agent-default-model # magpie
  config:
    model: magpie/m1
`)
	for i := 0; i < 40; i++ {
		b.WriteString("- id: user-entry-")
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString(string(rune('0' + i/26)))
		b.WriteString("\n  config:\n    step: done\n    note: keep me\n")
	}
	return b.String(), b.String()[:200]
}

// The desktop's write lands while the round is between its two reads: the
// round sees the list move and leaves it to the next round, which works
// from every entry.
func TestDshRouteAgainSkipsAFileBeingWritten(t *testing.T) {
	_, _, web := dshRouteHome(t)
	whole, half := dshHalfPair()
	if err := os.WriteFile(web, []byte(half), 0o644); err != nil {
		t.Fatal(err)
	}
	if n := len(magpieModels("dsh")); n == 0 {
		t.Skip("no catalog to write routes from")
	}
	// the body lands after the round's first read, inside its settle beat
	go func() {
		time.Sleep(150 * time.Millisecond)
		os.WriteFile(web, []byte(whole), 0o644)
	}()
	dshWiredOnce()
	b, _ := os.ReadFile(web)
	if !strings.Contains(string(b), "user-entry-z") {
		t.Fatalf("the half-state was fixed as the list — the user's entries are gone:\n%s", b[:min(700, len(b))])
	}
}
