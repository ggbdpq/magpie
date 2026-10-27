package agent

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
// `. "$HOME/.cargo/env"` under Debian's sh, where that file isn't there —
// ran the probe with nothing: every agent there was gone from the Agents
// page and nothing said why (#3754). The probe runs in the login shell and,
// when that shell has ended, in a plain one, so its agents are found.
func TestWSLProbeFindsAgentsWhenTheLoginShellEnds(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a Windows shell reports the temp folder as a drive")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	root := filepath.Join(home, "root")
	os.MkdirAll(root, 0o755)
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

	run, rootOf := wslRun, wslRoot
	reset := func() {
		wsl.Lock()
		wsl.seen, wsl.at, wsl.runAt, wsl.names, wsl.running, wsl.dirty, wsl.probed, wsl.failed = nil, time.Time{}, time.Time{}, nil, nil, false, nil, nil
		wsl.Unlock()
	}
	t.Cleanup(func() { wslRun, wslRoot = run, rootOf; reset() })
	reset()
	wslRun = func(_ time.Duration, args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case "-l -q", "-l --running -q":
			return []byte("Ubuntu-24.04\n"), nil
		}
		// the probe as wsl.exe runs it in the distro, in that distro's home
		cmd := exec.Command(sh, args[4:]...)
		cmd.Env = []string{"HOME=" + home, "PATH=" + shells}
		return cmd.Output()
	}
	wslRoot = func(name string) string { return root }

	var ids []string
	for _, a := range wslAgentsOf(wslDistros()) {
		ids = append(ids, a.ID)
	}
	if !strings.Contains(strings.Join(ids, " "), "claude@wsl:Ubuntu-24.04") {
		t.Fatalf("agents in the distro: %v", ids)
	}
}

// A probe that fails says so in the log with the exit status and the first
// line the distro's shell said: the status alone doesn't say that a profile
// ended the shell.
func TestWSLProbeWhySaysWhatTheShellSaid(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	_, err = exec.Command(sh, "-c", "echo 'sh: 10: .: cannot open /root/.cargo/env: No such file' >&2; echo again >&2; exit 2").Output()
	if err == nil {
		t.Fatal("no error")
	}
	why := wslProbeWhy(err)
	if !strings.Contains(why, "exit status 2") || !strings.Contains(why, "cannot open /root/.cargo/env") || strings.Contains(why, "again") {
		t.Fatalf("why = %q", why)
	}
}
