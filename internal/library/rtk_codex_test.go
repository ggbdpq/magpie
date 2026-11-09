package library

// Codex reads its hooks from hooks.json and from the [hooks] tables of its
// config.toml alike. #5711: a user may have written rtk's hook as
// [[hooks.PreToolUse]] there, which hooks.json's absence hid — the RTK page
// said off while Codex ran the hook, and switching RTK on would have run
// rtk's installer to add a second hook of the same command to hooks.json.
// And Codex's elevated Windows sandbox runs its commands as Codex's sandbox
// account, which reads rtk's history folder but can't write it (only an
// inherited (RX)), so a hook there rewrites the command, runs it, and
// silently records nothing (#5711). Codex makes every folder of
// [sandbox_workspace_write] writable_roots writable for its sandbox
// accounts when it starts, so magpie adds rtk's own folder there once,
// when RTK is switched on for Codex.

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

// fakeRTKCodex is an rtk whose installer writes Codex's hook into
// hooks.json, as rtk 1.0.0's does.
const fakeRTKCodex = `#!/bin/sh
case "$1" in
--version) echo 'rtk 1.0.0'; exit 0 ;;
gain) exit 1 ;;
init) echo '{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"rtk hook codex"}]}]}}' > "$HOME/.codex/hooks.json"; exit 0 ;;
esac
exit 2
`

func TestRTKCodexTomlHooks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake rtk is a shell script")
	}
	h := sandbox(t)
	codex := filepath.Join(h, ".codex")
	conf := filepath.Join(codex, "config.toml")
	bin := filepath.Join(h, "bin")
	testenv.Program(t, filepath.Join(bin, "rtk"), fakeRTKCodex)
	t.Setenv("PATH", bin)
	on := func() bool {
		t.Helper()
		for _, a := range ReadRTK().Agents {
			if a.ID == "codex" {
				return a.On
			}
		}
		t.Fatal("codex isn't listed")
		return false
	}
	if on() {
		t.Fatal("no hook anywhere, yet Codex showed on")
	}
	// a hook in config.toml alone: Codex runs it, so the page says on
	write(t, conf, "[[hooks.PreToolUse]]\ntype = \"command\"\nmatcher = \"Bash\"\ncommand = \"rtk hook codex\"\n")
	if !on() {
		t.Fatal("the hook is in config.toml's [hooks.PreToolUse], yet Codex showed off")
	}
	// switching it on again doesn't run rtk's installer, whose hooks.json
	// hook would be a second one of the same command
	if _, err := SetRTK("codex", true); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(codex, "hooks.json")) {
		t.Fatal("a hooks.json hook was written beside the one config.toml has")
	}
	// a hook in hooks.json alone still says on, as it did
	if err := os.Remove(conf); err != nil {
		t.Fatal(err)
	}
	if _, err := SetRTK("codex", true); err != nil {
		t.Fatal(err)
	}
	if !on() {
		t.Fatal("the hook is in hooks.json, yet Codex showed off")
	}
	// a config.toml hook that isn't rtk's is not one
	hooks := filepath.Join(codex, "hooks.json")
	_ = os.Remove(hooks)
	write(t, conf, "[[hooks.PreToolUse]]\ncommand = \"another hook\"\n")
	if on() {
		t.Fatal("another command's hook said rtk's")
	}
	// nor is rtk's command under an event that rewrites no command
	write(t, conf, "[[hooks.SessionStart]]\ncommand = \"rtk hook codex\"\n")
	if on() {
		t.Fatal("a hook under SessionStart said on")
	}
	// switching RTK off with only config.toml's hook there is said to be
	// the user's to take out, not pretended done
	write(t, conf, "[[hooks.PreToolUse]]\ncommand = \"rtk hook codex\"\n")
	if _, err := SetRTK("codex", false); err == nil || !strings.Contains(err.Error(), "config.toml") {
		t.Fatalf("switching off a hook only config.toml has: %v", err)
	}
	// and with hooks.json's hook there too, magpie takes its own out and
	// says config.toml's is the user's to take out
	if err := os.Remove(conf); err != nil {
		t.Fatal(err)
	}
	if _, err := SetRTK("codex", true); err != nil {
		t.Fatal(err)
	}
	write(t, conf, "[[hooks.PreToolUse]]\ncommand = \"rtk hook codex\"\n")
	if _, err := SetRTK("codex", false); err == nil || !strings.Contains(err.Error(), "config.toml") {
		t.Fatalf("switching off with both hooks: %v", err)
	}
	if contains(hooks, "rtk hook codex") {
		t.Fatal("hooks.json's hook was left in, though it is magpie's to take out")
	}
}

// Codex runs a matcher group's nested hooks array the same as a flat
// command: [[hooks.PreToolUse]] with [[hooks.PreToolUse.hooks]] beneath it
// was laweit-lxy's shape (trusted in Codex's hooks.state as
// config.toml:pre_tool_use:0:0), yet the flat-only read said off while
// Codex ran it (#5711).
func TestRTKCodexTomlNestedHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake rtk is a shell script")
	}
	h := sandbox(t)
	codex := filepath.Join(h, ".codex")
	conf := filepath.Join(codex, "config.toml")
	bin := filepath.Join(h, "bin")
	testenv.Program(t, filepath.Join(bin, "rtk"), fakeRTKCodex)
	t.Setenv("PATH", bin)
	on := func() bool {
		t.Helper()
		for _, a := range ReadRTK().Agents {
			if a.ID == "codex" {
				return a.On
			}
		}
		t.Fatal("codex isn't listed")
		return false
	}
	// the reporter's shape: a matcher group whose command lives in the
	// nested hooks array (command and its Windows form both set)
	write(t, conf, `[[hooks.PreToolUse]]
matcher = "Bash"

[[hooks.PreToolUse.hooks]]
type = "command"
command = "rtk hook codex"
commandWindows = "pwsh.exe -NoProfile -Command \"rtk hook codex\""
timeout = 10
`)
	if !on() {
		t.Fatal("the hook is in the nested [hooks.PreToolUse.hooks], yet Codex showed off")
	}
	// another command's nested hook is not rtk's
	write(t, conf, `[[hooks.PreToolUse]]
matcher = "Bash"

[[hooks.PreToolUse.hooks]]
type = "command"
command = "another hook"
`)
	if on() {
		t.Fatal("another command's nested hook said rtk's")
	}
	// commandWindows is the Windows-only form: off Windows, a nested hook
	// only that field sets isn't one Codex runs
	write(t, conf, `[[hooks.PreToolUse]]
matcher = "Bash"

[[hooks.PreToolUse.hooks]]
type = "command"
commandWindows = "pwsh.exe -NoProfile -Command \"rtk hook codex\""
`)
	if on() {
		t.Fatal("a nested hook only commandWindows sets said on off Windows")
	}
}

func TestRTKCodexGainWritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake rtk is a shell script")
	}
	h := sandbox(t)
	codex := filepath.Join(h, ".codex")
	conf := filepath.Join(codex, "config.toml")
	bin := filepath.Join(h, "bin")
	testenv.Program(t, filepath.Join(bin, "rtk"), fakeRTKCodex)
	t.Setenv("PATH", bin)
	local := filepath.Join(h, "local")
	t.Setenv("LOCALAPPDATA", local)
	was := sandboxedWindows
	sandboxedWindows = true
	t.Cleanup(func() { sandboxedWindows = was })
	// set switches RTK on for Codex afresh: its hook is taken out first,
	// so the installer runs again (switching on one already on is nothing)
	set := func() {
		t.Helper()
		os.Remove(filepath.Join(codex, "hooks.json"))
		if _, err := SetRTK("codex", true); err != nil {
			t.Fatal(err)
		}
	}
	roots := func() []string {
		t.Helper()
		b, err := os.ReadFile(conf)
		if err != nil {
			t.Fatal(err)
		}
		r, err := writableRootsIn(b)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	rtk := filepath.Join(local, "rtk")
	// no sandbox in config.toml: nothing to fix, and nothing written
	set()
	if b, err := os.ReadFile(conf); err != nil || len(b) != 0 {
		t.Fatalf("with no sandbox, config.toml changed: %q, %v", b, err)
	}
	// the elevated sandbox: switching RTK on for Codex adds rtk's history
	// folder to what Codex's sandbox account may write
	write(t, conf, "[windows]\nsandbox = \"elevated\"\n")
	set()
	if got := roots(); !slices.Equal(got, []string{rtk}) {
		t.Fatalf("writable_roots = %q, want rtk's folder alone", got)
	}
	// an unelevated sandbox isn't one this fix was tried with
	write(t, conf, "[windows]\nsandbox = \"unelevated\"\n")
	set()
	if got := roots(); got != nil {
		t.Fatalf("writable_roots = %q, want none", got)
	}
	// switching RTK off and on again adds the folder no second time, and
	// what the user had there stays first
	write(t, conf, "[windows]\nsandbox = \"elevated\"\n[sandbox_workspace_write]\nwritable_roots = [\""+filepath.Join(local, "elsewhere")+"\"]\n")
	set()
	set()
	if got := roots(); !slices.Equal(got, []string{filepath.Join(local, "elsewhere"), rtk}) {
		t.Fatalf("writable_roots = %q, want the user's and rtk's, once", got)
	}
	b, err := os.ReadFile(conf)
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(b, []byte(rtk)); n != 1 {
		t.Fatalf("rtk's folder is in config.toml %d times", n)
	}
}
