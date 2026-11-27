package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// TestHostStartsPastAnOverlongStderrLine runs the real host with the real Bun
// testenv finds (the one on PATH, at least BunVersion's MinBun), and a plugin
// whose load writes one 5 MiB line to stderr. The line stops the drain at
// 4 MiB; with the child's stderr left unread, on Windows its init answer never
// comes and startOn runs out of time. On Linux Bun keeps the unread bytes in
// memory instead and starts either way, so there TestDrainStderrKeepsTheChildGoing
// is what fails without the fix.
func TestHostStartsPastAnOverlongStderrLine(t *testing.T) {
	bun := testenv.Bun(t)
	dir := storeSandbox(t)
	plug := filepath.Join(dir, "plug.mjs")
	src := "console.log('x'.repeat(5 * 1024 * 1024))\nexport const name = 'overlong-stderr'\n"
	if err := os.WriteFile(plug, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	list, err := json.Marshal(map[string]any{"plugins": []map[string]string{{"spec": plug}}})
	if err != nil {
		t.Fatal(err)
	}
	writeList(t, string(list))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h, _, err := startOn(ctx, bun)
	if err != nil {
		t.Fatalf("the host did not start past the plugin's overlong stderr line: %v", err)
	}
	t.Cleanup(h.stop)
}

// A plugin whose own async work nobody awaits (dim 0.3.7's cache, #7868)
// rejects where nothing sees it: Bun's answer to an unhandled rejection
// is to quit, and with the host went every provider it serves — a model
// list and every turn of every conversation beside. The host says what
// rejected instead, and goes on.
func TestHostSurvivesAPluginsUnhandledRejection(t *testing.T) {
	bun := testenv.Bun(t)
	dir := storeSandbox(t)
	plug := filepath.Join(dir, "plug.mjs")
	src := "Promise.reject(new Error('unable to cache file[object Object]: No such file.'))\n" +
		"setTimeout(() => { Promise.reject(new Error('late rejection')) }, 300)\n" +
		"export const name = 'bad-reject'\n"
	if err := os.WriteFile(plug, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	list, err := json.Marshal(map[string]any{"plugins": []map[string]string{{"spec": plug}}})
	if err != nil {
		t.Fatal(err)
	}
	writeList(t, string(list))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h, _, err := startOn(ctx, bun)
	if err != nil {
		t.Fatalf("the host did not start past the plugin's unhandled rejection: %v", err)
	}
	t.Cleanup(h.stop)
	// a rejection the plugin fires long after loading kills nothing either
	time.Sleep(700 * time.Millisecond)
	if err := h.call(ctx, "prompt", map[string]any{"provider": "x", "method": "m", "inputs": map[string]any{}}, nil); err != nil && !strings.Contains(err.Error(), "no") {
		t.Errorf("the host stopped answering after the plugin's late rejection: %v", err)
	}
}
