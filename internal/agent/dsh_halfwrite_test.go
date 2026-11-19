package agent

// A fresh generation into dsh's patch list is written from a settled read:
// a list that reads empty or missing right after the file was touched may
// still be under its writer's hand, and a generation written from that
// would fix it as the list, the user's entries gone with it (#5782,
// stevenpisces: 57 entries became 2). The read is taken again after a beat,
// and what is written comes from the settled read.

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
	back := make(chan struct{})
	go func() {
		defer close(back)
		time.Sleep(150 * time.Millisecond)
		os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n"+dshUsersEntries(40)), 0o644)
	}()
	t.Cleanup(func() { <-back })
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
	// the desktop's save lands its body inside the settle beat
	back := make(chan struct{})
	go func() {
		defer close(back)
		time.Sleep(150 * time.Millisecond)
		os.WriteFile(desktop, []byte("# Your patch layer for this dsh profile.\n"+dshUsersEntries(40)), 0o644)
	}()
	t.Cleanup(func() { <-back })
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
	back := make(chan struct{})
	go func() {
		defer close(back)
		time.Sleep(150 * time.Millisecond)
		os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n"+dshUsersEntries(40)), 0o644)
	}()
	t.Cleanup(func() { <-back })
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

// The likeliest window an atomic writer leaves is not a half: the file is
// renamed over whole, and for the instant the swap happens it is missing —
// a read landing there parses to nothing (edit.Read gives no bytes for a
// missing file), while the user's entries were whole moments before (#5782,
// stevenpisces: 57 entries became 2). The settled read looks again after a
// beat — the swap has landed by then — and what the fresh generation writes
// comes from that; a file missing for good stays the generation's own.
func TestDshSettledReadMissingListIsReread(t *testing.T) {
	f := filepath.Join(t.TempDir(), "cordis.patch.yml")
	whole := "# Your patch layer for this dsh profile.\n" + dshUsersEntries(40)
	os.WriteFile(f, []byte(whole), 0o644)
	// the swap is played by hand: the file is gone when the read lands,
	// and back inside the settle beat
	os.Remove(f)
	back := make(chan struct{})
	go func() {
		defer close(back)
		time.Sleep(150 * time.Millisecond)
		os.WriteFile(f, []byte(whole), 0o644)
	}()
	t.Cleanup(func() { <-back })
	// the read the writer's swap hid, as dshSetFile's own gave it
	head, items, err := dshSettledRead(f, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 40 {
		t.Fatalf("a list missing at the read, whole moments before, was taken as gone — the fresh generation would have written over the user's %d entries", len(items))
	}
	if len(head) == 0 || !strings.Contains(head[0], "Your patch layer") {
		t.Fatalf("the settled read lost the list's head: %q", head)
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
