package gui

import (
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
)

// A restart that hands the gateway over waits only for the bridge's
// Claude Code turns, which can't go on in another process; one that
// doesn't waits for every request, as before (#577). The page is told
// which it is.
func TestRestartWaitsForWhatAHandoverCantCarry(t *testing.T) {
	at := time.Now().Add(-time.Second)
	b := gateway.Busy{Requests: 3, Tools: 1, Last: time.Now(), Bridge: 1, BridgeLast: at}
	if got := blocking(b, false); got != b {
		t.Fatalf("without a handover: %+v", got)
	}
	got := blocking(b, true)
	if got.Requests != 1 || got.Tools != 1 || !got.Last.Equal(at) {
		t.Fatalf("handing over, waits for %+v; want the bridge's turn and the parked tool", got)
	}
	if blocking(gateway.Busy{Requests: 4, Last: time.Now()}, true).Any() {
		t.Fatal("handing over, waits for requests that the old one finishes after it")
	}

	oldBusy, oldHands := gatewayBusy, handsOver
	t.Cleanup(func() { gatewayBusy, handsOver = oldBusy, oldHands })
	gatewayBusy = func() gateway.Busy { return gateway.Busy{Requests: 2} }
	u := &updater{state: "ready"}
	for _, hands := range []bool{false, true} {
		handsOver = func() bool { return hands }
		j := u.jsonIn("")
		if j.Handover != hands {
			t.Errorf("handover %v: the page reads %v", hands, j.Handover)
		}
		if (j.Busy != nil) == hands {
			t.Errorf("handover %v: the page shows busy %+v", hands, j.Busy)
		}
	}
}

// A restart hands over only from the app, on a Mac or Linux, with the
// gateway its own; otherwise it restarts as before.
func TestHandsOverOnlyWhereItCan(t *testing.T) {
	if handoverHost.Load() || served.Load() != nil {
		t.Skip("another test's gateway")
	}
	if handsOver() {
		t.Fatal("hands over outside the app")
	}
	oldExe := startExe
	t.Cleanup(func() { startExe = oldExe; handoverHost.Store(false) })
	startExe = "/apps/magpie"
	handoverHost.Store(true)
	if handsOver() {
		t.Fatal("hands over a gateway this magpie doesn't serve")
	}
	if beginHandover("", "/apps/magpie", false, "", false) || pending.Load() != nil {
		t.Fatal("began a handover it can't make")
	}
}

// restartSandbox gives a test a home, a gateway port of its own and no
// handover under way, so leave takes the plain restart's path.
func restartSandbox(t *testing.T) int {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("APPDATA", filepath.Join(h, "AppData"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	port := freePort(t)
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:"+strconv.Itoa(port))
	wasPending := pending.Load()
	pending.Store(nil)
	wasServed := served.Load()
	served.Store(nil)
	t.Cleanup(func() {
		gatewayMu.Lock()
		stopServedLocked()
		gatewayMu.Unlock()
		served.Store(wasServed)
		pending.Store(wasPending)
	})
	return port
}

// A restart that can't hand the gateway over quits to let the version it
// started serve in its place, and its quit takes the gateway off the port
// first (#5922): the port lives exactly as long as the process does, and
// the old app of an update on Windows whose exit wedged — hung inside
// ExitProcess with the gateway's Accept and Serve goroutines still
// parked — kept :3425 bound and unanswered until it was killed by hand.
// Here the gateway is served and answered on for real, and the quit only
// runs once the port is free.
func TestRestartQuitTakesTheGatewayOffThePort(t *testing.T) {
	port := restartSandbox(t)
	gatewayMu.Lock()
	gw := serveGatewayLocked()
	gatewayMu.Unlock()
	if gw == nil {
		t.Fatal("the gateway didn't start")
	}
	quits := make(chan struct{}, 1)
	leave(func() { quits <- struct{}{} })
	select {
	case <-quits:
	case <-time.After(10 * time.Second):
		t.Fatal("the restart's quit never ran")
	}
	if served.Load() != nil {
		t.Fatal("the quit ran with the gateway still served here")
	}
	for deadline := time.Now().Add(5 * time.Second); answers(port); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the gateway is still answered on after the quit ran")
		}
	}
}

// And a gateway that won't end doesn't hold the quit: the stop is asked
// for first, and past restartStopWait the quit runs anyway, cutting what
// is in flight as the exit would have cut it.
func TestRestartQuitRunsPastAGatewayThatWontEnd(t *testing.T) {
	restartSandbox(t)
	events := make(chan string, 2)
	oldWait := restartStopWait
	restartStopWait = 250 * time.Millisecond
	t.Cleanup(func() { restartStopWait = oldWait })
	gatewayMu.Lock()
	oldStop, oldDone := servedRun.stop, servedRun.done
	t.Cleanup(func() {
		gatewayMu.Lock()
		servedRun.stop, servedRun.done = oldStop, oldDone
		gatewayMu.Unlock()
	})
	served.Store(gateway.New()) // never started: nothing ends it
	servedRun.stop = func() { events <- "stop" }
	servedRun.done = make(chan struct{}) // never closed
	gatewayMu.Unlock()
	quits := make(chan struct{}, 1)
	leave(func() { events <- "quit"; quits <- struct{}{} })
	select {
	case <-quits:
	case <-time.After(10 * time.Second):
		t.Fatal("the restart's quit never ran past a gateway that won't end")
	}
	if got := <-events; got != "stop" {
		t.Fatalf("the quit ran before the gateway was asked to stop: %q first", got)
	}
	if got := <-events; got != "quit" {
		t.Fatalf("the gateway's stop came twice: %q again", got)
	}
}
