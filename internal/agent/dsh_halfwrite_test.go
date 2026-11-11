package agent

// dsh's desktop rewrites its own cordis.patch.yml in place — truncate, then
// the body — so for a moment the file is its head alone: magpie's route
// entries sit up front, and a round that read in that window saw a wired
// list of two and wrote that half-state back as the whole list. The user's
// other entries were gone for good, over and over (#5782, stevenpisces: 57
// entries became 2). A fresh list is reread after a beat; one that moved
// under us is left to the next round, which reads it whole.

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
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

// dshUsersEntries is the body a desktop's save lands: the user's own
// entries, no route of magpie's among them.
func dshUsersEntries(n int) string {
	return strings.Repeat("- id: user-entry\n  config:\n    step: done\n    note: keep me\n", n)
}

// The model-set path reads the same list the rounds do: one that reads as
// its comment head and no entries right after the file was touched is a
// writer's head, not dsh's own empty — the fresh generation written from it
// went back as head plus magpie's two entries, the user's entries gone with
// it (#5782, stevenpisces: the half matched 4 of 51 lines of the real
// list). The list is reread after a beat, and the pick lands on the
// settled read.
func TestDshSetWritesOnAListThatSettled(t *testing.T) {
	home, dir, web := dshRouteHome(t)
	dshKeyFixture(dir)
	os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n"), 0o644)
	// the desktop's save lands the body inside the settle beat
	go func() {
		time.Sleep(150 * time.Millisecond)
		os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n"+dshUsersEntries(40)), 0o644)
	}()
	a := dsh(home)
	if err := a.Field("model").Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(web)
	if s := string(b); strings.Count(s, "note: keep me") != 40 {
		t.Fatalf("the fresh generation fixed the half as the list — the user's entries are gone:\n%s", s[:min(700, len(s))])
	}
}

// A list that reads empty and stays empty is dsh's own: the fresh
// generation goes out as before, one beat later at most.
func TestDshSetWritesThroughAStableEmptyList(t *testing.T) {
	home, dir, web := dshRouteHome(t)
	dshKeyFixture(dir)
	template := "# Your patch layer for this dsh profile.\n[]\n"
	os.WriteFile(web, []byte(template), 0o644)
	past := time.Now().Add(-10 * time.Second)
	os.Chtimes(web, past, past)
	a := dsh(home)
	if err := a.Field("model").Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(web)
	for _, want := range []string{"id: llm-pi-ai", "displayName: Magpie", "provider: magpie", "id: deepseek/flash"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("a stable empty list not filled with %q:\n%s", want, b)
		}
	}
}

// and the round that fills a profile dsh has just made reads it settled:
// the desktop app's first save caught mid-write was the reporter's other
// half (#5782). The start here is the user's own, so the fill goes out
// through dshPutRoute and the write itself, not dshSetFile.
func TestDshFillNewProfilesWritesOnAListThatSettled(t *testing.T) {
	_, dir, web := dshRouteHome(t)
	dshKeyFixture(dir)
	os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n"+
		dshRouteFixture("deepseek/pro", "deepseek/flash")+
		"- id: agent-default-model\n  config:\n    provider: deepseek-official\n    model: deepseek-v4-pro\n"), 0o644)
	desktop := filepath.Join(dir, "profiles", "desktop", "cordis.patch.yml")
	os.MkdirAll(filepath.Dir(desktop), 0o755)
	os.WriteFile(desktop, []byte("# Your patch layer for this dsh profile.\n"), 0o644)
	// the desktop's save lands its body a round behind the fill's first
	// read, inside the settle beat
	go func() {
		time.Sleep(500 * time.Millisecond)
		os.WriteFile(desktop, []byte("# Your patch layer for this dsh profile.\n"+dshUsersEntries(40)), 0o644)
	}()
	if err := dshSync(dir, gateway.URL()); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(desktop)
	s := string(b)
	if strings.Count(s, "note: keep me") != 40 {
		t.Fatalf("the fill fixed the half as the list — the user's entries are gone:\n%s", s[:min(700, len(s))])
	}
	if !strings.Contains(s, "      magpie:\n") {
		t.Fatalf("the fill did not land on the settled list:\n%s", s[:min(900, len(s))])
	}
}

// and the effort pick the same: agent-default-model generated on a
// head-only read went back alone (#5782). The start the pick keeps sits in
// the home layer while the profile's own list is dsh's save caught
// mid-write.
func TestDshSetEffortWritesOnAListThatSettled(t *testing.T) {
	home, dir, web := dshRouteHome(t)
	dshKeyFixture(dir)
	hp := filepath.Join(dir, "cordis.patch.yml")
	os.WriteFile(hp, []byte("# shared by every profile\n"+
		"- id: agent-default-model # magpie\n  config:\n    provider: deepseek-official\n    model: deepseek-v4-pro\n"), 0o644)
	os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n"), 0o644)
	// the desktop's save lands the body inside the settle beat
	go func() {
		time.Sleep(150 * time.Millisecond)
		os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n"+dshUsersEntries(40)), 0o644)
	}()
	a := dsh(home)
	if err := a.Field("effort").Set("high"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(web)
	s := string(b)
	if strings.Count(s, "note: keep me") != 40 {
		t.Fatalf("the effort pick fixed the half as the list — the user's entries are gone:\n%s", s[:min(700, len(s))])
	}
	if !strings.Contains(s, "reasoningEffort: \"high\"") {
		t.Fatalf("the effort did not land on the settled list:\n%s", s[:min(700, len(s))])
	}
}

// The model-set path wrote silently; one line there would have found the
// loss in minutes (stevenpisces). A pick says what it wrote where.
func TestDshSetSaysWhatItWrote(t *testing.T) {
	home, dir, web := dshRouteHome(t)
	dshKeyFixture(dir)
	os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n[]\n"), 0o644)
	past := time.Now().Add(-10 * time.Second)
	os.Chtimes(web, past, past)
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	a := dsh(home)
	if err := a.Field("model").Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "wrote dsh's route in "+web) {
		t.Fatalf("the model-set write said nothing:\n%s", buf.String())
	}
}
