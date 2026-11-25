package gateway

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// hookWrites is a KillHookEnv command writing RunIDEnv's value to out.
func hookWrites(out string) string {
	if runtime.GOOS == "windows" {
		return `echo %MAGPIE_RUN_ID%>"` + out + `"`
	}
	return `printf '%s\n' "$MAGPIE_RUN_ID" > '` + out + `'`
}

// hookedFor waits for the kill hook's file, then says what it wrote.
func hookedFor(t *testing.T, out string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if b, err := os.ReadFile(out); err == nil {
			return strings.TrimSpace(string(b))
		}
		if time.Now().After(deadline) {
			t.Fatalf("the kill hook never wrote %s", out)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The kill hook is the user's command for the runs of this machine, told
// the ended run's tag in its environment (RunIDEnv): what a wrapper's
// exec'd Claude Code needs ended where the group kill cannot reach
// (#7747). Said, or for a run without a tag, it does nothing.
func TestClaudeKillHookEndsWithTheRunID(t *testing.T) {
	out := filepath.Join(t.TempDir(), "hooked")
	t.Setenv(KillHookEnv, hookWrites(out))
	runKillHook("magpie-claude-123")
	if got := hookedFor(t, out); got != "magpie-claude-123" {
		t.Fatalf("the hook was told %q, not the run's tag", got)
	}
}

func TestClaudeKillHookSaidDoesNothing(t *testing.T) {
	t.Setenv(KillHookEnv, "")
	runKillHook("magpie-claude-123") // nothing to look for: only that it says so
	runKillHook("")
}

// A run ended — its client went away, it waited as long as it may, it was
// let go for another, it ended itself — runs the hook once, with its own
// tag: finish is where every ending funnels.
func TestClaudeRunEndsOnItsKillHook(t *testing.T) {
	out := filepath.Join(t.TempDir(), "hooked")
	t.Setenv(KillHookEnv, hookWrites(out))
	r := &subscriptionRun{runID: "magpie-claude-test"}
	r.finish()
	if got := hookedFor(t, out); got != "magpie-claude-test" {
		t.Fatalf("the hook was told %q, not the run's tag", got)
	}
	r.finish()
	time.Sleep(300 * time.Millisecond)
	if b, err := os.ReadFile(out); err != nil || strings.Count(strings.TrimSpace(string(b)), "magpie-claude-test") != 1 {
		t.Fatalf("the hook ran again: %q (%v)", b, err)
	}
}

// A run's Claude Code is told the run's tag (RunIDEnv), and once the run
// ends the kill hook is run with it: a wrapper that execs the real Claude
// Code in another pid namespace (`docker exec` into a container) is ended
// there by the command it names, which magpie's group kill cannot reach
// (#7747).
func TestClaudeKillHookEndsTheRunItsCommandNames(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	dir := t.TempDir()
	claudeEnv := filepath.Join(dir, "claude-env")
	hooked := filepath.Join(dir, "hooked")
	script := `#!/bin/sh
echo "$MAGPIE_RUN_ID" > "$MAGPIE_TEST_CLAUDE_ENV"
n=0
while read -r line; do
  n=$((n+1))
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-sonnet-5","usage":{"input_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pid '$$' turn '$n'"}}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
done
`
	testenv.Program(t, filepath.Join(dir, "claude"), script)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	runsEndWithTest(t, dir)
	t.Setenv("MAGPIE_TEST_CLAUDE_ENV", claudeEnv)
	t.Setenv(KillHookEnv, hookWrites(hooked))
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	s := New()
	t.Cleanup(s.subscription.abortAll)
	p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "me@example.com"}}
	body := `{"model":"claude-opus-4-1","max_tokens":100,"tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"a subagent's task"}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	var u Usage
	if code, msg := s.serveClaudeSubscription(rec, req, provider.Anthropic, p, "claude-opus-4-1", []byte(body), &u); code != 200 {
		t.Fatalf("%d %s", code, msg)
	}
	tag := hookedFor(t, claudeEnv)
	if !strings.HasPrefix(tag, "magpie-claude-") {
		t.Fatalf("Claude Code was told %q, not the run's tag", tag)
	}
	s.subscription.abortAll()
	if got := hookedFor(t, hooked); got != tag {
		t.Fatalf("the hook was told %q, not the run's tag %q", got, tag)
	}
}
