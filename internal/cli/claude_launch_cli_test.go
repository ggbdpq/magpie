package cli

import (
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/applog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// claude-magpie (baranwang on X): `magpie claude run` starts the user's
// claude with --settings and magpie's launch file, every argument
// claude's, --help too; the user's ~/.claude/settings.json is left as it
// was, a magpie model a session's /model saved there is taken back to the
// launch file, and claude's exit code is magpie's.
func TestClaudeRun(t *testing.T) {
	home := helpHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Anthropic: "https://example.test", Key: "k",
		Models: []string{"claude-opus-4-6", "claude-haiku-4-5", "glm-4.6"}}); err != nil {
		t.Fatal(err)
	}
	user := filepath.Join(home, ".claude", "settings.json")
	own := `{"model": "opus"}`
	os.MkdirAll(filepath.Dir(user), 0o755)
	os.WriteFile(user, []byte(own), 0o644)

	var bin string
	var args []string
	pick := ""
	exit := error(nil)
	oldExec, oldFind, oldUp, oldStart, oldGo := claudeRunExec, claudeFind, claudeGatewayUp, startUp, claudeLetGo
	t.Cleanup(func() {
		claudeRunExec, claudeFind, claudeGatewayUp, startUp, claudeLetGo = oldExec, oldFind, oldUp, oldStart, oldGo
	})
	let := 0
	claudeLetGo = func() { let++ }
	real := filepath.Join(home, "bin", "claude")
	claudeFind = func() string { return real }
	claudeGatewayUp = func() bool { return true }
	started := 0
	startUp = func() { started++ }
	claudeRunExec = func(b string, a, _ []string) error {
		bin, args = b, a
		if pick != "" { // what /model leaves in the user's file
			edit.SetJSON(user, edit.KV{Path: "model", Value: pick})
		}
		return exit
	}

	launch := agent.ClaudeLaunchPath()
	for _, in := range [][]string{nil, {"--help"}, {"-p", "hi", "--model", "sonnet"}} {
		if _, err := runOut(t, append([]string{"claude", "run"}, in...)...); err != nil {
			t.Fatal(err)
		}
		want := append([]string{"--settings", launch}, in...)
		if bin != real || !slices.Equal(args, want) {
			t.Fatalf("claude run %v: %s %v, want %v", in, bin, args, want)
		}
	}
	if let == 0 {
		t.Fatal("magpie's probes and plugin hosts were left running beside claude")
	}
	if started == 0 {
		t.Fatal("claude run started before magpie's start-up")
	}
	if v, _ := edit.GetJSON(launch, "env.ANTHROPIC_BASE_URL"); v != gateway.URL() {
		t.Fatalf("launch file's base URL %q", v)
	}
	if b, _ := os.ReadFile(user); string(b) != own {
		t.Fatalf("the user's settings.json was written:\n%s", b)
	}

	pick = "relay/glm-4.6"
	if _, err := runOut(t, "cc", "run"); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetJSON(user, "model"); v != "opus" {
		t.Fatalf("the user's model after a /model pick of magpie's: %q", v)
	}
	if v, _ := edit.GetJSON(launch, "model"); v != "relay/glm-4.6" {
		t.Fatalf("the launch file's model after the pick: %q", v)
	}

	pick = ""
	exit = exitError{code: 3}
	var ee exitError
	if _, err := runOut(t, "claude", "run"); !errors.As(err, &ee) || ee.code != 3 {
		t.Fatalf("claude's exit 3 is %v", err)
	}

	out, err := runOut(t, "claude", "--help")
	if err != nil || !strings.Contains(out, "magpie claude run [args…]") || !strings.Contains(out, "fields:") {
		t.Fatalf("magpie claude --help: %v\n%s", err, out)
	}

	claudeFind = func() string { return "" }
	if _, err := runOut(t, "claude", "run"); err == nil {
		t.Fatal("no error without Claude Code")
	}
}

// The terminal is claude's for a claude-magpie session: what magpie logs
// beside it — an account's models asked again, the shape of the report's
// lines (#5923) — goes to magpie's log, not to that screen, and the
// logger is back on stderr once the session is done. What the launcher
// itself has to say (the gateway down) is still on the terminal.
func TestClaudeRunLogsBesideTheSessionGoToTheLog(t *testing.T) {
	home := helpHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Anthropic: "https://example.test", Key: "k",
		Models: []string{"claude-opus-4-6"}}); err != nil {
		t.Fatal(err)
	}
	oldExec, oldFind, oldUp, oldStart, oldGo := claudeRunExec, claudeFind, claudeGatewayUp, startUp, claudeLetGo
	t.Cleanup(func() {
		claudeRunExec, claudeFind, claudeGatewayUp, startUp, claudeLetGo = oldExec, oldFind, oldUp, oldStart, oldGo
	})
	claudeFind = func() string { return filepath.Join(home, "bin", "claude") }
	claudeGatewayUp = func() bool { return false } // the launcher has a word for the user
	startUp = func() {}
	claudeLetGo = func() {}

	before := log.Writer()
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldErr := os.Stderr
	os.Stderr = errW
	t.Cleanup(func() { os.Stderr = oldErr })

	// the session: claude is on its screen while this runs
	inSession := make(chan io.Writer, 1)
	resume := make(chan struct{})
	claudeRunExec = func(b string, a, _ []string) error {
		log.Printf("antigravity someone@example.com: fetchAvailableModels at https://daily-cloudcode-pa.googleapis.com: 20 models, 13 kept")
		inSession <- log.Writer()
		<-resume
		return nil
	}

	done := make(chan error, 1)
	go func() {
		_, err := runOut(t, "claude", "run")
		done <- err
	}()
	var beside io.Writer
	select {
	case beside = <-inSession:
	case <-time.After(20 * time.Second):
		t.Fatal("claude never started")
	}
	if beside == before {
		t.Error("the logger still wrote to the terminal beside claude's session")
	}
	logged, err := os.ReadFile(applog.Path())
	if err != nil {
		t.Fatalf("reading magpie's log: %v", err)
	}
	if !strings.Contains(string(logged), "fetchAvailableModels") {
		t.Errorf("magpie's log has no line from beside the session:\n%s", logged)
	}
	close(resume) // the session is over
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	errW.Close()
	said, _ := io.ReadAll(errR)
	if !strings.Contains(string(said), "the gateway isn't running") {
		t.Errorf("the launcher's word for a gateway down isn't on the terminal:\n%s", said)
	}
	if log.Writer() != before {
		t.Fatalf("the logger is at %v, want it back where it was", log.Writer())
	}
}
