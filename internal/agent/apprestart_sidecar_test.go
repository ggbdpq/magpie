package agent

import (
	"context"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// The reopen waits for the app's own process, not every helper under the
// bundle: WorkBuddy AI leaves a detached sidecar running when its GUI
// quits — it holds the IPC sockets for a next start — and waiting for it
// ran out the whole wait every time, so Restart quit the app and never
// opened it again (#3700).
func TestReopenBundleWaitsForTheAppAlone(t *testing.T) {
	home := restartSandbox(t)
	apps := filepath.Join(home, "Applications")
	bundle := macApp(t, apps, "WorkBuddy AI.app", "com.workbuddy.workbuddy-ai", "Electron")
	restartOnMac = true
	var pats []string
	running = func(pat string) []runningProc {
		pats = append(pats, pat)
		// the GUI quit; the sidecar it left under the bundle keeps running
		sidecar := bundle + "/Contents/Resources/app.asar/main/sidecar-entry.js --token x"
		if regexp.MustCompile(pat).MatchString(sidecar) {
			return []runningProc{{time.Hour, sidecar}}
		}
		return nil
	}
	a := workbuddyAI(home)
	appQuit = func(_ context.Context, b string) error { return nil }
	var opened []string
	appOpen = func(_ context.Context, b string) error { opened = append(opened, b); return nil }
	appQuitWait = 200 * time.Millisecond
	if err := reopenBundle(context.Background(), a, bundle, workbuddyAIApp); err != nil {
		t.Fatalf("a sidecar left behind held the reopen: %v (asked %q)", err, pats)
	}
	if len(opened) != 1 || opened[0] != bundle {
		t.Fatalf("opened %q, want the app back once", opened)
	}
	if !slices.ContainsFunc(pats, func(p string) bool {
		return strings.Contains(p, `WorkBuddy AI\.app/Contents/MacOS/Electron`)
	}) {
		t.Fatalf("not asked by the bundle's executable: %q", pats)
	}
}
