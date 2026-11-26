package plugin

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPathPluginEditRestartsTheHost (yansheng1003 on X: "每次改了需要重新
// magpie"): a plugin added by its path is the user's own code, edited in
// place. The host running had loaded it as it was, and an edit reached it
// only once magpie was quit and opened again. A change to any of its files
// (not its node_modules or .git) now starts the host again; nothing
// changed, or a plugin turned off, does not.
func TestPathPluginEditRestartsTheHost(t *testing.T) {
	storeSandbox(t)
	every := pathsEvery
	pathsEvery = 0 // every look walks: the test is about what is seen, not how often
	t.Cleanup(func() { pathsEvery = every })

	dir := filepath.Join(t.TempDir(), "my-plugin")
	for _, d := range []string{"src", "node_modules/dep", ".git"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	then := time.Now().Add(-time.Hour)
	write := func(rel, body string, at time.Time) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	write("package.json", `{"main":"src/index.js"}`, then)
	write("src/index.js", `export default async () => ({})`, then)
	write("node_modules/dep/index.js", `1`, then)
	write(".git/HEAD", `ref: refs/heads/main`, then)
	file := filepath.Join(t.TempDir(), "solo.js")
	if err := os.WriteFile(file, []byte(`export default async () => ({})`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, then, then); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(List{Plugins: []Entry{{Spec: dir}, {Spec: "file://" + file}}})
	writeList(t, string(b))

	prevStale := hostStale.Swap(false)
	t.Cleanup(func() { hostStale.Store(prevStale) })
	checkList() // the app has seen the plugins as they are
	hostStale.Store(false)
	stale := func() bool { checkList(); return hostStale.Swap(false) }

	if stale() {
		t.Fatal("nothing changed, and the host is stale")
	}
	write("node_modules/dep/index.js", `2`, then.Add(time.Minute))
	write(".git/HEAD", `ref: refs/heads/other`, then.Add(time.Minute))
	if stale() {
		t.Fatal("only its dependencies and .git changed, and the host was started again")
	}
	write("src/index.js", `export default async () => ({ v: 2 })`, then.Add(2*time.Minute))
	if !stale() {
		t.Fatal("the plugin's own code was edited, and the running host goes on with the old code")
	}
	if stale() {
		t.Fatal("one edit restarted the host twice")
	}
	if err := os.WriteFile(file, []byte(`export default async () => ({ v: 2 })`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !stale() {
		t.Fatal("a plugin that is one file was edited, and the running host goes on with the old code")
	}

	// this magpie's own restart takes up the edit: no second restart
	write("src/index.js", `export default async () => ({ v: 3 })`, then.Add(3*time.Minute))
	Restart()
	if stale() {
		t.Fatal("this magpie's own restart was taken for an edit")
	}

	// an edit to a plugin turned off restarts nothing
	b, _ = json.Marshal(List{Plugins: []Entry{{Spec: dir, Off: true}}})
	writeList(t, string(b))
	stale()
	write("src/index.js", `export default async () => ({ v: 4 })`, then.Add(4*time.Minute))
	if stale() {
		t.Fatal("a plugin turned off was edited, and the host was started again")
	}
}

// TestPathsLookedAtOnceASecond: checkList runs on every call to a plugin,
// and a path plugin's folder is walked; the walk is made again only once
// pathsEvery has gone by, or plugins.json changed.
func TestPathsLookedAtOnceASecond(t *testing.T) {
	storeSandbox(t)
	every := pathsEvery
	pathsEvery = time.Hour
	t.Cleanup(func() { pathsEvery = every })
	dir := t.TempDir()
	p := filepath.Join(dir, "index.js")
	if err := os.WriteFile(p, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(List{Plugins: []Entry{{Spec: dir}}})
	writeList(t, string(b))
	first := pathsStamp()
	at := time.Now().Add(time.Minute)
	if err := os.WriteFile(p, []byte("22"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(p, at, at)
	if pathsStamp() != first {
		t.Fatal("the folder was walked again inside pathsEvery")
	}
	pathsEvery = 0
	if pathsStamp() == first {
		t.Fatal("the edit wasn't seen once pathsEvery went by")
	}
}

// TestPathPluginRestartLoopCircuitBreaker (#7198): a local path plugin that
// keeps writing inside its own folder must log the triggering plugin/file and
// trip a per-plugin auto-reload circuit breaker rather than restarting the
// shared plugin host forever, while leaving other path plugins working and
// letting an explicit Restart resume watching.
func TestPathPluginRestartLoopCircuitBreaker(t *testing.T) {
	storeSandbox(t)
	every := pathsEvery
	pathsEvery = 0
	t.Cleanup(func() { pathsEvery = every })

	var logs bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(oldOut) })

	loopDir := filepath.Join(t.TempDir(), "loop-plugin")
	otherDir := filepath.Join(t.TempDir(), "other-plugin")
	for _, d := range []string{filepath.Join(loopDir, "page"), otherDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Now().Add(-time.Hour)
	writeAt := func(p, body string, at time.Time) {
		t.Helper()
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	dataFile := filepath.Join(loopDir, "page", "data.js")
	otherFile := filepath.Join(otherDir, "index.js")
	writeAt(dataFile, "v0", base)
	writeAt(otherFile, "v0", base)

	b, _ := json.Marshal(List{Plugins: []Entry{{Spec: loopDir}, {Spec: otherDir}}})
	writeList(t, string(b))

	prevStale := hostStale.Swap(false)
	t.Cleanup(func() { hostStale.Store(prevStale) })
	checkList()
	hostStale.Store(false)
	stale := func() bool { checkList(); return hostStale.Swap(false) }

	reloads := 0
	for i := 1; i <= 8; i++ {
		writeAt(dataFile, strings.Repeat("x", i+2), base.Add(time.Duration(i)*time.Second))
		if stale() {
			reloads++
		}
	}
	if reloads != 5 {
		t.Fatalf("loop plugin triggered %d reloads out of 8 writes, want circuit breaker at 5", reloads)
	}
	gotLog := logs.String()
	if !strings.Contains(gotLog, "loop-plugin") || !strings.Contains(gotLog, "data.js") {
		t.Errorf("log did not name triggering plugin and file: %q", gotLog)
	}
	if !strings.Contains(gotLog, "paused auto-reload") {
		t.Errorf("log did not report paused auto-reload: %q", gotLog)
	}

	// Another path plugin must still hot-reload normally while loop-plugin is paused.
	writeAt(otherFile, "v1-updated", base.Add(time.Minute))
	if !stale() {
		t.Fatal("pausing loop-plugin blocked reloads for other-plugin")
	}

	// Explicit Restart clears the paused state so loop-plugin reloads again.
	Restart()
	hostStale.Store(false)
	writeAt(dataFile, "after-restart", base.Add(2*time.Minute))
	if !stale() {
		t.Fatal("explicit Restart did not resume path watching for loop-plugin")
	}
}
