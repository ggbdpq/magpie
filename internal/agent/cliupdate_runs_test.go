package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

// An npm update that exits 0 but leaves Claude Code without its native
// package (#2128): the package says 2.1.299, and bin/claude.exe is the
// stub that only says the native binary isn't installed. magpie installs
// that version again with its optional packages, and when that doesn't
// mend it either, the update fails, saying how to reinstall it.
func TestUpdateCLIChecksItRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts as the CLI")
	}
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	prefix := filepath.Join(tmp, "homebrew")
	pkgDir := filepath.Join(prefix, "lib/node_modules/@anthropic-ai/claude-code")
	pkgJSON := filepath.Join(pkgDir, "package.json")
	exeFile := filepath.Join(pkgDir, "bin/claude.exe")
	// the stub as Claude Code's postinstall leaves it without the native package
	stub := "#!/bin/sh\necho 'Error: claude native binary not installed.' >&2\nexit 1\n"
	works := "#!/bin/sh\necho '2.1.299 (Claude Code)'\n"
	file(t, pkgJSON, `{"name":"@anthropic-ai/claude-code","version":"2.1.298"}`)
	file(t, exeFile, works)
	npm := file(t, filepath.Join(prefix, "bin/npm"), "#!/bin/sh\nexit 1\n")
	link(t, "../lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe", filepath.Join(prefix, "bin/claude"))
	t.Setenv("PATH", filepath.Join(prefix, "bin"))
	t.Setenv("HOME", filepath.Join(tmp, "home"))

	oldRun := runUpdate
	t.Cleanup(func() { runUpdate = oldRun; versions, latests = memo{}, memo{} })
	versions, latests = memo{}, memo{}
	latests.get("npm:@anthropic-ai/claude-code", "", 1<<40, 1<<40, func() (string, error) { return "2.1.299", nil })
	var ran []string
	mends := true
	runUpdate = func(_ context.Context, u *updater) ([]byte, error) {
		ran = append(ran, strings.Join(u.cmd, " "))
		// npm 11.19.0's log: "reify failed optional dependency …-darwin-arm64", exit 0
		os.WriteFile(pkgJSON, []byte(`{"name":"@anthropic-ai/claude-code","version":"2.1.299"}`), 0o644)
		body := stub
		if mends && strings.HasSuffix(u.cmd[len(u.cmd)-1], "--include=optional") {
			body = works
		}
		testenv.WriteProgram(exeFile, body)
		return []byte("changed 1 package in 3s"), nil
	}
	a := &Agent{ID: "claude", Name: "Claude Code", Bin: "claude"}
	if c, ok := a.CLI(); !ok || c.Via != "npm" || !c.Update || c.Version != "2.1.298" {
		t.Fatalf("before: %+v", c)
	}

	// mended by installing 2.1.299 again with its optional packages
	c, err := a.UpdateCLI()
	if err != nil || c.Version != "2.1.299" {
		t.Fatalf("mended: %+v, %v", c, err)
	}
	want := []string{
		npm + " install -g --prefix " + prefix + " @anthropic-ai/claude-code@latest",
		npm + " install -g --prefix " + prefix + " @anthropic-ai/claude-code@2.1.299 --include=optional",
	}
	if strings.Join(ran, "\n") != strings.Join(want, "\n") {
		t.Errorf("ran:\n%s\nwant:\n%s", strings.Join(ran, "\n"), strings.Join(want, "\n"))
	}

	// a working update runs nothing more
	ran = nil
	os.WriteFile(pkgJSON, []byte(`{"name":"@anthropic-ai/claude-code","version":"2.1.298"}`), 0o644)
	versions = memo{}
	runUpdate = func(_ context.Context, u *updater) ([]byte, error) {
		ran = append(ran, strings.Join(u.cmd, " "))
		os.WriteFile(pkgJSON, []byte(`{"name":"@anthropic-ai/claude-code","version":"2.1.299"}`), 0o644)
		return nil, nil
	}
	if _, err := a.UpdateCLI(); err != nil || len(ran) != 1 {
		t.Fatalf("a working update: %v, ran %q", err, ran)
	}

	// not mended: the update fails, saying what claude said and the reinstall
	ran = nil
	mends = false
	os.WriteFile(pkgJSON, []byte(`{"name":"@anthropic-ai/claude-code","version":"2.1.298"}`), 0o644)
	versions = memo{}
	runUpdate = func(_ context.Context, u *updater) ([]byte, error) {
		ran = append(ran, strings.Join(u.cmd, " "))
		os.WriteFile(pkgJSON, []byte(`{"name":"@anthropic-ai/claude-code","version":"2.1.299"}`), 0o644)
		testenv.WriteProgram(exeFile, stub)
		return nil, nil
	}
	_, err = a.UpdateCLI()
	if err == nil || !strings.Contains(err.Error(), "claude native binary not installed") ||
		!strings.Contains(err.Error(), "npm install -g --prefix "+prefix+" @anthropic-ai/claude-code@2.1.299 --include=optional") || len(ran) != 2 {
		t.Fatalf("not mended: %v, ran %q", err, ran)
	}
}

// What can't be told isn't taken for broken: a CLI that says its version
// and exits non-zero, one whose interpreter isn't found (127), one that
// prints nothing and exits 0.
func TestRunsBrokenOnlyWhenSure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts as the CLI")
	}
	dir := t.TempDir()
	u := &updater{via: "npm", cmd: []string{"npm"}}
	n := 0
	for body, broken := range map[string]bool{
		"#!/bin/sh\necho 'Error: claude native binary not installed.'\nexit 1\n": true,
		"#!/bin/sh\necho 'tool 1.2.3'\nexit 2\n":                                 false,
		"#!/bin/sh\necho 'env: node: No such file or directory' >&2\nexit 127\n": false,
		"#!/bin/sh\n": false,
	} {
		n++
		bin := file(t, filepath.Join(dir, fmt.Sprint("cli", n)), body)
		if got := runsBroken(context.Background(), bin, u); (got != "") != broken {
			t.Errorf("%q: broken %q, want %v", body, got, broken)
		}
	}
	if got := runsBroken(context.Background(), filepath.Join(dir, "missing"), u); got != "" {
		t.Errorf("a CLI that didn't start: %q", got)
	}
}

// claudeStub is @anthropic-ai/claude-code 2.1.302's bin/claude.exe, the
// 500 bytes npm leaves when its optional native package isn't fetched. It
// has no shebang: a shell runs it, the kernel won't (#2798).
const claudeStub = `echo "Error: claude native binary not installed." >&2
echo "" >&2
echo "Either postinstall did not run (--ignore-scripts, some pnpm configs)" >&2
echo "or the platform-native optional dependency was not downloaded" >&2
echo "(--omit=optional)." >&2
echo "" >&2
echo "Run the postinstall manually (adjust path for local vs global install):" >&2
echo "  node node_modules/@anthropic-ai/claude-code/install.cjs" >&2
echo "" >&2
echo "Or reinstall without --ignore-scripts / --omit=optional." >&2
exit 1
`

// The real Claude Code stub can't be exec'd at all: no shebang, so Go says
// "exec format error" with no output and no exit code. That is the broken
// install #2128 is about, not a run that can't be told about (#2798).
func TestRunsBrokenSeesAShebanglessStub(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no shebang-less stub: npm's shim runs the .exe through cmd.exe")
	}
	bin := file(t, filepath.Join(t.TempDir(), "claude.exe"), claudeStub)
	for _, pkg := range []string{"@anthropic-ai/claude-code", "opencode-ai"} {
		u := &updater{via: "npm", pkg: pkg, cmd: []string{"npm"}}
		if got := runsBroken(context.Background(), bin, u); !strings.Contains(got, "exec format error") {
			t.Errorf("%s: a stub the kernel won't run: %q, want it said as broken", pkg, got)
		}
	}
	// a bin the kernel won't run for a package that doesn't ship such a
	// stub is still unknown, not broken: its update may be fine
	u := &updater{via: "npm", pkg: "some-other-cli", cmd: []string{"npm"}}
	if got := runsBroken(context.Background(), bin, u); got != "" {
		t.Errorf("a bin that didn't start for another package: %q", got)
	}
}
