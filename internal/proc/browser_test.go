package proc

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Windows' rundll32 is named by its path in the system folder: by its bare
// name a PATHEXT without .EXE (WSL passed one on as ".CPL") found nothing,
// and the sign-in's page never opened (#2043, xiaobaifly7).
func TestBrowserNamesRundll32ByItsPath(t *testing.T) {
	url := "https://mcp.cloudflare.com/authorize?state=a&b=c"
	name, args := browserCommand("windows", `C:\WINDOWS`, url)
	if want := filepath.Join(`C:\WINDOWS`, "System32", "rundll32.exe"); name != want {
		t.Fatalf("launcher %q, want %q", name, want)
	}
	if len(args) != 2 || args[0] != "url.dll,FileProtocolHandler" || args[1] != url {
		t.Fatalf("args %q", args)
	}
	// no SystemRoot to go by: the bare name, as before
	if name, _ := browserCommand("windows", "", url); name != "rundll32" {
		t.Fatalf("without SystemRoot: %q", name)
	}
	for goos, want := range map[string]string{"darwin": "open", "linux": "xdg-open", "android": "termux-open-url"} {
		if name, args := browserCommand(goos, "", url); name != want || len(args) != 1 || args[0] != url {
			t.Errorf("%s: %q %q", goos, name, args)
		}
	}
}

// A magpie on a server, in a container or over SSH has no display for
// xdg-open, and a browser started there only comes up and goes away
// (#5958): none starts, and the error says why, for the caller to show
// with the link it prints. A display keeps the browser, as do WSL (its
// xdg-open is wslview, which opens Windows' own browser with no Linux
// display) and the systems whose launcher reaches a screen of its own:
// darwin's open, Windows' rundll32 and termux-open-url.
func TestBrowserWithoutDisplayStartsNoneAndSaysWhy(t *testing.T) {
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	start := startBrowser
	defer func() { startBrowser = start }()
	var started int
	startBrowser = func(*exec.Cmd) error { started++; return nil }
	kernel := osrelease
	defer func() { osrelease = kernel }()

	// WSL is spared: its launcher opens Windows' own browser
	osrelease = func() (string, bool) { return "5.15.167.4-microsoft-standard-WSL2", true }
	if err := openBrowserOn("linux", "https://accounts.google.com/o/oauth2/authorize"); err != nil {
		t.Fatalf("WSL: %v", err)
	}
	// a plain server's launcher has no display to open a browser on
	osrelease = func() (string, bool) { return "6.8.0-85-generic", true }
	err := openBrowserOn("linux", "https://accounts.google.com/o/oauth2/authorize")
	if err == nil || started != 1 || !strings.Contains(err.Error(), "no display") {
		t.Fatalf("no display: err %v, started %d", err, started)
	}
	// a display, X11's or Wayland's, keeps the browser
	t.Setenv("DISPLAY", ":0")
	if err := openBrowserOn("linux", "https://accounts.google.com/o/oauth2/authorize"); err != nil || started != 2 {
		t.Fatalf("X11: err %v, started %d", err, started)
	}
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	if err := openBrowserOn("linux", "https://accounts.google.com/o/oauth2/authorize"); err != nil || started != 3 {
		t.Fatalf("Wayland: err %v, started %d", err, started)
	}
	t.Setenv("WAYLAND_DISPLAY", "")
	// darwin's open, Windows' rundll32 and termux-open-url need no display here
	for _, goos := range []string{"darwin", "windows", "android"} {
		if err := openBrowserOn(goos, "https://accounts.google.com/o/oauth2/authorize"); err != nil {
			t.Fatalf("%s: %v", goos, err)
		}
	}
	if started != 6 {
		t.Fatalf("browsers started: %d", started)
	}
}

// inWSL reads the kernel's name: WSL1 and WSL2 both report a microsoft
// one; anything else, or a name that can't be read, is not WSL.
func TestInWSL(t *testing.T) {
	kernel := osrelease
	defer func() { osrelease = kernel }()
	for name, want := range map[string]bool{
		"5.15.167.4-microsoft-standard-WSL2": true,
		"4.4.0-19041-Microsoft":              true,
		"6.8.0-85-generic":                   false,
	} {
		osrelease = func() (string, bool) { return name, true }
		if got := inWSL(); got != want {
			t.Errorf("%s: %v, want %v", name, got, want)
		}
	}
	osrelease = func() (string, bool) { return "", false }
	if inWSL() {
		t.Error("a kernel whose name can't be read: WSL")
	}
}
