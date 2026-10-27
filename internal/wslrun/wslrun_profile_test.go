package wslrun

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A distro whose profile ends its login shell — the reporter's
// `. "$HOME/.cargo/env"`, where that file isn't there — answered a probe
// with nothing, so a command there was taken as missing (#3754). Find looks
// in a plain shell when the login one has ended, and finds it.
func TestFindDistroWhoseLoginProfileEndsTheShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a Windows shell reports the temp folder as a drive")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// the distro's PATH holds only a shell: this machine's own commands
	// aren't the distro's
	shells := t.TempDir()
	if err := os.Symlink(sh, filepath.Join(shells, "sh")); err != nil {
		t.Fatal(err)
	}
	// the reporter's line, then, for a shell that reads on (bash), the same
	// ending: the login shell is gone at the profile either way
	profile := ". \"$HOME/.cargo/env\"\nexit 2\n"
	if err := os.WriteFile(filepath.Join(home, ".profile"), []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}

	oldOn, oldRun := On, Run
	t.Cleanup(func() { On, Run = oldOn, oldRun; Forget() })
	On = true
	Forget()
	Run = func(_ time.Duration, args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case "-l -q", "-l --running -q":
			return utf16le("Ubuntu-24.04\r\n"), nil
		}
		// the probe as wsl.exe runs it in the distro, in that distro's home
		cmd := exec.Command(sh, args[4:]...)
		cmd.Env = []string{"HOME=" + home, "PATH=" + shells}
		return cmd.Output()
	}

	tool, ok := Find("claude")
	if !ok || tool.Distro != "Ubuntu-24.04" || tool.Path != filepath.Join(bin, "claude") {
		t.Fatalf("Find = %+v %v", tool, ok)
	}
}
