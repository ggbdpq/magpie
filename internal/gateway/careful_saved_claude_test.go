package gateway

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// carefulSavedClaude signs in two Claude subscription accounts, the first
// (its own sign-in) and a saved one (backup@example.com, logins.json), and
// serves them through a claude CLI that answers a prompt with "pong" and a
// /usage with one window, each run naming its token in the log it keeps.
func carefulSavedClaude(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	far := time.Now().Add(24 * time.Hour).UnixMilli()
	oauth := func(tok string) map[string]any {
		return map[string]any{"claudeAiOauth": map[string]any{"accessToken": tok, "refreshToken": "r-" + tok, "expiresAt": far, "subscriptionType": "max"}}
	}
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), mustJSON(oauth("tok-primary")), 0o600)
	os.WriteFile(filepath.Join(home, ".claude.json"), mustJSON(map[string]any{
		"oauthAccount": map[string]any{"emailAddress": "primary@example.com", "accountUuid": "u-primary"}}), 0o600)
	os.MkdirAll(filepath.Dir(provider.Path()), 0o755)
	os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"), mustJSON([]map[string]any{
		{"agent": "claude", "user": "primary@example.com", "plan": "max", "on": true, "seen": time.Now(), "auth": oauth("tok-primary")},
		{"agent": "claude", "user": "backup@example.com", "plan": "max", "on": true, "seen": time.Now(), "auth": oauth("tok-backup")},
	}), 0o600)
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	script := `#!/bin/sh
if [ "$1" = auth ]; then echo '{"loggedIn":true,"authMethod":"oauth_token","apiProvider":"firstParty"}'; exit 0; fi
TOK=; [ -n "$CLAUDE_CONFIG_DIR" ] && TOK=$(sed -n 's/.*"accessToken": *"\([^"]*\)".*/\1/p' "$CLAUDE_CONFIG_DIR/.credentials.json")
echo "${TOK:-own}" >> ` + log + `
if [ "$2" = "/usage" ]; then
  # the first account's five hours are past the cap set on it; the saved
  # account's are at 30%, under it (#5085)
  USED=95; [ "$TOK" = tok-backup ] && USED=30
  printf '{"type":"result","subtype":"success","is_error":false,"result":"Current session: %s%% used"}\n' "$USED"
  exit 0
fi
while read -r line; do
  case $line in *'"type":"control_request"'*)
    id=$(printf '%s' "$line" | sed 's/.*"request_id":"\([^"]*\)".*/\1/')
    echo '{"type":"control_response","response":{"subtype":"success","request_id":"'$id'"}}'
    continue ;;
  esac
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-sonnet-5","usage":{"input_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pong"}}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":"pong"}'
done
`
	testenv.Program(t, filepath.Join(dir, "claude"), script)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)
	return log
}

// miniersliu's #5085, after a restart: two Claude accounts, each capped at
// 90% of its five hours, careful on, the first (the one Claude Code is
// signed in to) past its cap and the saved one at 30% with nothing read
// since magpie started. A saved account's /usage runs for no ask of
// routing's, so careful's re-read came back as what was kept and the saved
// account was held for good while the first sat past its cap: every account
// held, the agent refused, nothing sent. The re-read ask now reads the
// saved account for real (its 30% is under the cap), and the request goes
// to it.
func TestCarefulRereadsASavedClaudeAccount(t *testing.T) {
	log := carefulSavedClaude(t)
	carefulTried.Lock()
	carefulTried.at = nil
	carefulTried.Unlock()
	old := allowances
	allowances = provider.Allowances
	t.Cleanup(func() { allowances = old })
	if err := provider.SetRouting("claude", provider.Ordered); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"primary@example.com", "backup@example.com"} {
		if err := provider.SetAccountCap("claude", u, 90); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SetCapCareful("claude", true); err != nil {
		t.Fatal(err)
	}
	five, week := time.Now().Add(2*time.Hour).Unix(), time.Now().Add(4*24*time.Hour).Unix()
	provider.NoteClaudeLimits("primary@example.com", []provider.ClaudeLimit{{Kind: "five_hour", Used: 0.95, ResetsAt: five}, {Kind: "seven_day", Used: 0.1, ResetsAt: week}})
	t.Cleanup(func() {
		provider.StaleAllowance("claude", "primary@example.com")
		provider.StaleAllowance("claude", "backup@example.com")
	})
	// nothing read of the saved account since magpie started: routing
	// knows it only as one whose usage couldn't be read, as careful sees
	// it — wait for that state, so the request below starts from it
	provider.Allowances("claude")
	waitFor(t, "the saved account held as unread", func() bool {
		_, failed, ok := provider.UsageRead("claude", "backup@example.com")
		return ok && failed
	})
	s := New()
	code, body := sendTo(s, "/v1/messages", `{"model":"claude/claude-sonnet-5","max_tokens":50,"stream":true,"messages":[{"role":"user","content":"ping"}]}`)
	b, _ := os.ReadFile(log)
	if code != 200 || !strings.Contains(body, "pong") {
		t.Fatalf("status %d: %s (runs %q)", code, body, b)
	}
	if !strings.Contains(string(b), "tok-backup") {
		t.Fatalf("the saved account was never read nor served (runs %q)", b)
	}
	var tried []string
	for _, rt := range s.Trace(t.Context(), 0, 0).Routes {
		for _, try := range rt.Tries {
			tried = append(tried, try.ID)
		}
	}
	if !slices.Contains(tried, "claude@backup@example.com") {
		t.Fatalf("tried %v; want the saved account serving the request", tried)
	}
}
