//go:build !nogui

package gui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// #5773 (BHXGGGGG): on Windows Wails reports a WebView2 initialisation
// failure through ErrorHandler and then exits the whole process — the
// reporter's log shows the controller wait timing out after 1m0s and
// magpie 0.1.3226 ending with it. The errors are the strings Wails'
// edge/chromium.go hands errorCallback before its os.Exit(1). Errors
// reported without exiting (a shortcut that didn't register, an event
// handler's panic) are none of these.
func TestWebView2Fatal(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"the reporter's timeout", errors.New("magpie: timed out after 1m0s waiting for the WebView2 controller; the WebView2 runtime did not complete initialisation"), true},
		{"loader call failed", errors.New("error calling Webview2Loader: no webview2 found"), true},
		{"runtime version failed", errors.New("error getting Webview2 runtime version: some status"), true},
		{"browser path gone", fmt.Errorf("browser path '%s' does not exist", `C:\missing`), true},
		{"controller create failed", errors.New("error creating controller with 800713de: The object was not found."), true},
		{"shortcut not fatal", fmt.Errorf("failed to register global shortcut %q: %w", "Ctrl+R", errors.New("taken")), false},
		{"event error not fatal", errors.New("event handler failed: boom"), false},
		{"none", nil, false},
	} {
		if got := webView2Fatal(c.err); got != c.want {
			t.Errorf("%s: webView2Fatal=%v, want %v", c.name, got, c.want)
		}
	}
}

// The exit box names WebView2, says magpie is about to end, and asks
// whether to start it again, in each language the app speaks, English
// where it has none.
func TestWebView2ExitNotice(t *testing.T) {
	for _, lang := range []string{"en", "zh", "zh-TW", "ja", "de", "pt"} {
		title, body := webView2ExitNotice(lang)
		if !strings.Contains(title, "WebView2") || !strings.Contains(body, "WebView2") {
			t.Errorf("%s: doesn't name WebView2: %q / %q", lang, title, body)
		}
		if !strings.Contains(body, "magpie web") || !strings.Contains(body, "magpie tui") {
			t.Errorf("%s: doesn't say what works without windows: %q", lang, body)
		}
		if strings.Contains(body, "%!") {
			t.Errorf("%s: bad format: %q", lang, body)
		}
		if !strings.HasSuffix(body, "?") && !strings.HasSuffix(body, "？") {
			t.Errorf("%s: doesn't end asking whether to start again: %q", lang, body)
		}
	}
}

// The exit asks in a box on a thread of its own (#1806: a box on Wails'
// window thread never finishes), gives Yes to starting magpie again, and
// tidies up onShutdown before Wails' exit either way: os.Exit skips
// OnShutdown, and the failover Codex was left pointing at this process
// would retry a gateway that is gone. A box nobody answers within the
// wait ends the same way, not as a hung magpie.
func TestWebview2ExitAskRestartTidyUp(t *testing.T) {
	for _, c := range []struct {
		name       string
		answer     bool
		answerIn   time.Duration
		wait       time.Duration
		restartErr error
		want       []string
	}{
		{"yes restarts then tidies", true, 0, time.Second, nil,
			[]string{"restart", "shutdown"}},
		{"no tidies without restart", false, 0, time.Second, nil,
			[]string{"shutdown"}},
		{"restart failing still tidies", true, 0, time.Second, errors.New("exe gone"),
			[]string{"restart", "shutdown"}},
		{"a box nobody answers tidies alone", false, 20 * time.Millisecond, 5 * time.Millisecond,
			nil, []string{"shutdown"}},
	} {
		var order []string
		var restarted bool
		runWebview2Exit(
			func() bool {
				if c.answerIn > 0 {
					time.Sleep(c.answerIn)
				}
				return c.answer
			},
			func() error {
				restarted = true
				order = append(order, "restart")
				return c.restartErr
			},
			func() { order = append(order, "shutdown") },
			c.wait,
		)
		if len(order) != len(c.want) {
			t.Fatalf("%s: ran %v, want %v", c.name, order, c.want)
		}
		for i, who := range c.want {
			if order[i] != who {
				t.Fatalf("%s: ran %v, want %v", c.name, order, c.want)
			}
		}
		if wantRestart := c.answer && c.answerIn <= c.wait; restarted != wantRestart {
			t.Errorf("%s: restarted=%v, want %v", c.name, restarted, wantRestart)
		}
	}
}
