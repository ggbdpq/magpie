package proc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestHideWithoutConsole(t *testing.T) {
	defer func(f func() bool) { hasConsole = f }(hasConsole)

	hasConsole = func() bool { return false }
	if cmd := Command("cmd", "/c", "ver"); cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Fatalf("no console: CREATE_NO_WINDOW not set: %+v", cmd.SysProcAttr)
	}
	cmd := CommandContext(context.Background(), "cmd", "/c", "ver")
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Fatalf("no console: CREATE_NO_WINDOW not set: %+v", cmd.SysProcAttr)
	}
	if out, err := cmd.Output(); err != nil || len(out) == 0 {
		t.Fatalf("a hidden child's output should still be piped back: %q, %v", out, err)
	}

	hasConsole = func() bool { return true }
	if cmd := Command("cmd", "/c", "ver"); cmd.SysProcAttr != nil {
		t.Fatalf("with a console the child should share it: %+v", cmd.SysProcAttr)
	}
}

// A .cmd in a folder with a space, given an argument with a space, hands
// the program it runs the arguments as given: npm.cmd under nvm-windows'
// C:\Program Files\nodejs, installing into that same folder, had cmd.exe
// run "C:\Program" instead (wztlink1013 on Discord). The script passes %*
// on, as npm.cmd does to node, to this test binary, which prints what it
// was given. A " in an argument isn't among them: it comes back doubled to
// Go's parser, which reads "" the way C runtimes did before 2008.
func TestBatchFileInFolderWithSpace(t *testing.T) {
	if os.Getenv("MAGPIE_PROC_ECHO") == "1" {
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "Program Files", "nodejs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "@echo off\r\n\"" + self + "\" -test.run=TestEchoArgs -- %*\r\n"
	for _, name := range []string{"npm.cmd", "tool.bat"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"install", "-g", "--prefix", dir, "@anthropic-ai/claude-code@latest",
		"x&y", "a b|c", "%PATH%", `C:\a dir\`, `C:\b\`, "", "^(<>)!,;=", "中文 路径"}
	want, _ := json.Marshal(args)
	t.Setenv("MAGPIE_PROC_ECHO", "1")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, name := range []string{filepath.Join(dir, "npm.cmd"), filepath.Join(dir, "tool.bat"), "npm"} {
		for _, cmd := range []*exec.Cmd{Command(name, args...), CommandContext(context.Background(), name, args...)} {
			out, err := cmd.CombinedOutput()
			got, _, _ := strings.Cut(string(out), "\n")
			if err != nil || got != string(want) {
				t.Errorf("%s: got %q, %v; want %s", name, out, err, want)
			}
		}
	}
}

// TestEchoArgs is the program TestBatchFileInFolderWithSpace's scripts run.
func TestEchoArgs(t *testing.T) {
	if os.Getenv("MAGPIE_PROC_ECHO") != "1" {
		t.Skip("run by TestBatchFileInFolderWithSpace")
	}
	i := slices.Index(os.Args, "--")
	b, _ := json.Marshal(append([]string{}, os.Args[i+1:]...))
	fmt.Fprintf(os.Stdout, "%s\n", b)
	os.Exit(0)
}

// a probe still asking when magpie exits is ended by EndProbes, and
// EndProbes returns once its kill is over, not after its whole wait (#1278)
func TestEndProbesWindows(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := ProbeContext(ctx, "ping", "-n", "60", "127.0.0.1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, _ = io.Copy(io.Discard, out)
		done <- cmd.Wait()
	}()
	time.Sleep(500 * time.Millisecond) // asking, not starting
	start := time.Now()
	EndProbes()
	if d := time.Since(start); d > endWait {
		t.Fatalf("EndProbes took %v over a probe it killed", d)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the probe went on after EndProbes")
	}
}

// fnm's Node a terminal reaches through its PATH: %APPDATA%\fnm's versions
// (npx.cmd sits in the installation itself), highest first, with FNM_DIR's
// folder ahead of those when the profile set it (#2685).
func TestUserBinDirsFnm(t *testing.T) {
	roaming := filepath.Join(t.TempDir(), "AppData", "Roaming")
	t.Setenv("APPDATA", roaming)
	t.Setenv("FNM_DIR", "")
	t.Setenv("PATH", `C:\Windows\not-a-folder`)
	install := func(fnm, v string) string {
		d := filepath.Join(fnm, "node-versions", v, "installation")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "npx.cmd"), []byte("@echo off\r\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return d
	}
	fnm := filepath.Join(roaming, "fnm")
	install(fnm, "v9.11.2")
	v22 := install(fnm, "v22.12.0")
	var got []string
	for _, d := range UserBinDirs() {
		if strings.HasPrefix(d, fnm) {
			got = append(got, d)
		}
	}
	want := []string{v22, filepath.Join(fnm, "node-versions", "v9.11.2", "installation")}
	if !slices.Equal(got, want) {
		t.Fatalf("fnm's folders %q, want %q", got, want)
	}
	if p, want := FindTool("npx"), filepath.Join(v22, "npx.cmd"); p != want {
		t.Fatalf("found %q, want %q", p, want)
	}
	moved := filepath.Join(t.TempDir(), "fnm")
	movedV20 := install(moved, "v20.18.0")
	t.Setenv("FNM_DIR", moved)
	got = nil
	for _, d := range UserBinDirs() {
		if strings.HasPrefix(d, fnm) || strings.HasPrefix(d, moved) {
			got = append(got, d)
		}
	}
	want = []string{movedV20, v22, filepath.Join(fnm, "node-versions", "v9.11.2", "installation")}
	if !slices.Equal(got, want) {
		t.Fatalf("with FNM_DIR: %q, want %q", got, want)
	}
}
