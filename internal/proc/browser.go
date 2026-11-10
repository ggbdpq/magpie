package proc

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// OpenBrowser opens url in the user's default browser. The error is the
// launcher's failing to start, or there being no display to open one on,
// for the caller to show with the link to open by hand: a sign-in that
// waits on a page that never opened only times out (#2043), and a browser
// started on a machine with no screen only comes up and goes away there
// (#5958).
func OpenBrowser(url string) error { return openBrowserOn(runtime.GOOS, url) }

// openBrowserOn is OpenBrowser for the system goos names; tests stand in
// the start and the kernel's name.
func openBrowserOn(goos, url string) error {
	if why := noDisplay(goos, os.Getenv("DISPLAY"), os.Getenv("WAYLAND_DISPLAY"), inWSL()); why != "" {
		return errors.New(why)
	}
	name, args := browserCommand(goos, os.Getenv("SystemRoot"), url)
	return startBrowser(Command(name, args...))
}

// noDisplay says why a browser can't be shown on the machine magpie runs
// on, "" when one can: where the launcher is xdg-open (the default here),
// a server, a container or an SSH session with neither DISPLAY nor
// WAYLAND_DISPLAY has no browser to start, short of WSL, whose xdg-open
// (wslview) opens Windows' own browser. darwin's open, Windows' rundll32
// and termux-open-url all reach a screen of theirs without a display here.
func noDisplay(goos, display, wayland string, wsl bool) string {
	switch goos {
	case "darwin", "windows", "android":
		return ""
	}
	if display != "" || wayland != "" || wsl {
		return ""
	}
	return "there is no display on this machine to open a browser on"
}

// inWSL says whether magpie runs inside WSL, by the kernel's name: WSL1
// and WSL2 both report a microsoft one.
func inWSL() bool {
	b, ok := osrelease()
	return ok && strings.Contains(strings.ToLower(b), "microsoft")
}

// osrelease is this machine's kernel name, whether it could be read;
// tests stand in for it.
var osrelease = func() (string, bool) {
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	return string(b), err == nil
}

// startBrowser starts cmd and reaps it once it ends; tests stand in for it.
var startBrowser = func(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() //nolint:errcheck
	return nil
}

// browserCommand is the program and arguments that open url on goos. On
// Windows rundll32 is named by its path in the system folder: by its bare
// name Go looks it up with PATHEXT's extensions, and a PATHEXT without
// .EXE (one WSL passed on as ".CPL", #2043) finds none, though it is there.
func browserCommand(goos, systemRoot, url string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", []string{url}
	case "windows":
		exe := "rundll32"
		if systemRoot != "" {
			exe = filepath.Join(systemRoot, "System32", "rundll32.exe")
		}
		return exe, []string{"url.dll,FileProtocolHandler", url}
	case "android":
		return "termux-open-url", []string{url}
	default:
		return "xdg-open", []string{url}
	}
}
