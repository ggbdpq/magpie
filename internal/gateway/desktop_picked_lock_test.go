package gateway

import (
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// #5430 (dongsheng123132): desktopMoveTo's catalog work — desktopDefault's
// stand-in here, asleep for 60ms — ran under desktopPicked's lock, so
// concurrent Desktop requests were served one by one ("held Ns, 30s of it
// in no wait magpie knows of"). The lock keeps the picked fields alone
// now: the sleeps overlap and the requests don't queue.
func TestDesktopPickedLockKeepsCatalogWorkOut(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	desktopPicked.Lock()
	desktopPicked.model, desktopPicked.from, desktopPicked.at = "", "", time.Time{}
	desktopPicked.Unlock()
	t.Cleanup(func() {
		desktopPicked.Lock()
		desktopPicked.model, desktopPicked.from, desktopPicked.at = "", "", time.Time{}
		desktopPicked.Unlock()
	})
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Anthropic: "http://127.0.0.1:1",
		Models: []string{"m1", "claude-opus-4-8"}}); err != nil {
		t.Fatal(err)
	}
	StandIn = func(agent, model string) string {
		if agent == "claude-desktop" {
			time.Sleep(60 * time.Millisecond) // the catalog's work, as a stand-in
			return "fake/m1"
		}
		return ""
	}
	t.Cleanup(func() { StandIn = nil })
	title := []byte(`{"max_tokens":200,"messages":[{"role":"user","content":"hi"}]}`)
	const n = 10
	bad := make(chan string, 1)
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := desktopTurn("claude-haiku-4-5", title, false, false); got != "fake/m1" {
				bad <- got
			}
		}()
	}
	wg.Wait()
	took := time.Since(start)
	select {
	case got := <-bad:
		t.Fatalf("title = %q, want fake/m1", got)
	default:
	}
	// serialized, the ten 60ms reads stack to ~600ms; beside the lock they
	// overlap to about one
	if took > 300*time.Millisecond {
		t.Fatalf("%d concurrent titles took %s: the catalog work still holds desktopPicked's lock", n, took)
	}
}
