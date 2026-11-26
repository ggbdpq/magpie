package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
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

// TestPathOnlyUIPluginReloadDoesNotNotifyCatalog (#7671): swapping or rolling
// back a UI-only local path plugin must invalidate the host without firing
// OnChange/OnListed catalog-sync notifications when the provider/model list is
// unchanged, while a reload that actually changes a plugin's models still
// notifies OnListed.
func TestPathOnlyUIPluginReloadDoesNotNotifyCatalog(t *testing.T) {
	storeSandbox(t)
	every := pathsEvery
	pathsEvery = 0
	t.Cleanup(func() { pathsEvery = every })
	defer ListInBackground(ListInBackground(false))

	dir := filepath.Join(t.TempDir(), "ui-plugin")
	if err := os.MkdirAll(filepath.Join(dir, "page"), 0o755); err != nil {
		t.Fatal(err)
	}
	pageFile := filepath.Join(dir, "page", "app.js")
	base := time.Now().Add(-time.Hour)
	writePage := func(body string, at time.Time) {
		t.Helper()
		if err := os.WriteFile(pageFile, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(pageFile, at, at); err != nil {
			t.Fatal(err)
		}
	}
	writePage("console.log('build A')", base)
	b, _ := json.Marshal(List{Plugins: []Entry{{Spec: dir}}})
	writeList(t, string(b))

	// Seed the baseline provider list (no providers contributed by the UI plugin).
	commitProviders([]Provider{}, nil)
	Told()
	checkList()
	Told()
	prevStale := hostStale.Swap(false)
	t.Cleanup(func() { hostStale.Store(prevStale) })

	var changes, listings atomic.Int32
	onChangeMu.Lock()
	oldChange := onChange
	onChange = append(append([]func(){}, oldChange...), func() { changes.Add(1) })
	onChangeMu.Unlock()
	onListedMu.Lock()
	oldListed := onListed
	onListed = append(append([]func(){}, oldListed...), func() { listings.Add(1) })
	onListedMu.Unlock()
	t.Cleanup(func() {
		Told()
		onChangeMu.Lock()
		onChange = oldChange
		onChangeMu.Unlock()
		onListedMu.Lock()
		onListed = oldListed
		onListedMu.Unlock()
	})

	// Swap build A -> build B: host becomes stale, but zero catalog notifications fire.
	writePage("console.log('build B - longer asset')", base.Add(time.Minute))
	if !checkList() || !hostStale.Swap(false) {
		t.Fatal("swap did not invalidate host")
	}
	commitProviders([]Provider{}, nil)
	Told()
	if got := changes.Load() + listings.Load(); got != 0 {
		t.Fatalf("UI-only swap scheduled %d catalog notifications (onChange=%d, onListed=%d), want 0", got, changes.Load(), listings.Load())
	}

	// Roll back build B -> build A: still zero catalog notifications.
	writePage("console.log('build A')", base.Add(2*time.Minute))
	if !checkList() || !hostStale.Swap(false) {
		t.Fatal("rollback did not invalidate host")
	}
	commitProviders([]Provider{}, nil)
	Told()
	if got := changes.Load() + listings.Load(); got != 0 {
		t.Fatalf("UI-only rollback scheduled %d catalog notifications (onChange=%d, onListed=%d), want 0", got, changes.Load(), listings.Load())
	}

	// A reload that actually adds/changes a provider model MUST notify OnListed.
	writePage("console.log('build C with model')", base.Add(3*time.Minute))
	if !checkList() || !hostStale.Swap(false) {
		t.Fatal("provider-changing reload did not invalidate host")
	}
	commitProviders([]Provider{{Spec: dir, ID: "demo", Models: []Model{{ID: "m1"}}}}, nil)
	Told()
	if listings.Load() != 1 {
		t.Fatalf("provider/model change fired %d OnListed notifications, want 1", listings.Load())
	}
}
