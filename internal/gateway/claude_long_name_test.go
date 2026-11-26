package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// A Claude model of the 5 generation on that Claude Code doesn't know
// (Fable 5.2 on 2.1.316) is asked for by its "[1m]" name, which Claude
// Code runs to 1M: by its own it held the model to 200K and turned a
// longer conversation away before Anthropic was asked, so Codex, told the
// model's 1M window and compacting at 272K, never compacted (magicalaciw18
// on X). One it knows, and every 4 generation one, whose 1M a subscription
// without usage credits is refused a short turn on, keep their names.
func TestClaudeRunName(t *testing.T) {
	for model, want := range map[string]string{
		"claude-fable-5-2":     "claude-fable-5-2[1m]",
		"claude-opus-6":        "claude-opus-6[1m]",
		"claude-sonnet-5-7":    "claude-sonnet-5-7[1m]",
		"claude-fable-5-2[1m]": "claude-fable-5-2[1m]",
		"claude-fable-5-1":     "claude-fable-5-1",
		"claude-fable-5":       "claude-fable-5",
		"claude-opus-5-5":      "claude-opus-5-5",
		"claude-sonnet-5-5":    "claude-sonnet-5-5",
		"claude-haiku-5-5":     "claude-haiku-5-5",
		"claude-mythos-5-1":    "claude-mythos-5-1",
		"claude-sonnet-4-6":    "claude-sonnet-4-6",
		"claude-opus-4-8":      "claude-opus-4-8",
		"claude-haiku-4-5":     "claude-haiku-4-5",
		"claude-3-7-sonnet":    "claude-3-7-sonnet",
		"haiku":                "haiku",
		"m":                    "m",
	} {
		if got := claudeRunName(model); got != want {
			t.Errorf("claudeRunName(%q) = %q, want %q", model, got, want)
		}
	}
}

// The run of a Codex turn on claude/claude-fable-5-2 asks Claude Code for
// claude-fable-5-2[1m], and the reply names the model as asked.
func TestClaudeRunAsksUnknownModelLong(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	script := `#!/bin/sh
printf '%s\n' "$@" >> '` + argsFile + `'
while read -r line; do
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"x","usage":{"input_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
done
`
	testenv.Program(t, filepath.Join(dir, "claude"), script)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	runsEndWithTest(t, dir)
	s := New()
	t.Cleanup(s.subscription.abortAll)
	p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
	for _, tc := range []struct{ asked, model, want string }{
		{"claude-fable-5-2", "claude-fable-5-2", "claude-fable-5-2[1m]"},
		{"claude-sonnet-4-6", "claude-sonnet-4-6", "claude-sonnet-4-6"},
		{"claude/claude-opus-4-6[1m]", "claude-opus-4-6", "claude-opus-4-6[1m]"},
		{"claude/claude-sonnet-4-6[1m]", "claude-sonnet-4-6", "claude-sonnet-4-6[1m]"},
	} {
		_ = os.Remove(argsFile)
		body := `{"model":"` + tc.asked + `","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
		rec := httptest.NewRecorder()
		var u Usage
		if code, msg := s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, p, tc.model, []byte(body), &u); code != 200 {
			t.Fatalf("%s: %d %s", tc.asked, code, msg)
		}
		b, err := os.ReadFile(argsFile)
		if err != nil {
			t.Fatal(err)
		}
		args := strings.Split(string(b), "\n")
		got := ""
		for i, a := range args {
			if a == "--model" && i+1 < len(args) {
				got = args[i+1]
			}
		}
		if got != tc.want {
			t.Errorf("%s: Claude Code was asked for %q, want %q", tc.asked, got, tc.want)
		}
		var res struct{ Model string }
		json.Unmarshal(rec.Body.Bytes(), &res)
		if strings.Contains(res.Model, "[1m]") {
			t.Errorf("%s: the reply names %q", tc.asked, res.Model)
		}
	}
}

// Explicit "[1m]" requests keep the suffix for models Claude Code holds to
// 200K without it (claude-opus-4-6, claude-sonnet-4-6), while bare gen-4
// requests stay unmarked and known-1M models keep their bare ids (#7586).
func TestClaudeRunNameFor(t *testing.T) {
	for _, tc := range []struct {
		model string
		long  bool
		want  string
	}{
		{"claude-opus-4-6", false, "claude-opus-4-6"},
		{"claude-opus-4-6", true, "claude-opus-4-6[1m]"},
		{"claude-sonnet-4-6", false, "claude-sonnet-4-6"},
		{"claude-sonnet-4-6", true, "claude-sonnet-4-6[1m]"},
		{"claude-fable-5-2", false, "claude-fable-5-2[1m]"},
		{"claude-fable-5-2", true, "claude-fable-5-2[1m]"},
		{"claude-opus-5-5", true, "claude-opus-5-5"},
	} {
		if got := claudeRunNameFor(tc.model, tc.long); got != tc.want {
			t.Errorf("claudeRunNameFor(%q, %t) = %q, want %q", tc.model, tc.long, got, tc.want)
		}
	}
}

// An explicit "[1m]" request (e.g. claude/claude-opus-4-6[1m]) must preserve
// the 1M mark onto the launched CLI model arg and must not share a session
// key with a bare 200K request for the same resolved model (#7586).
func TestClaudeLongContextSessionKey(t *testing.T) {
	capture := func(asked string) *Request {
		t.Helper()
		s := New()
		t.Cleanup(s.subscription.abortAll)
		body := `{"model":"` + asked + `","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
		var got *Request
		s.serveSubscription(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)),
			provider.Anthropic, "Claude Code", "claude-opus-4-6", "claude", []byte(body), &Usage{},
			func(_ context.Context, req *Request) (*subscriptionRun, <-chan Event, error) {
				got = req
				return nil, nil, errors.New("stop")
			})
		if got == nil {
			t.Fatalf("%s: start was not called", asked)
		}
		return got
	}
	modelArg := func(args []string) string {
		for i, a := range args {
			if a == "--model" && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}
	bare := capture("claude/claude-opus-4-6")
	long := capture("claude/claude-opus-4-6[1m]")
	if got := modelArg(claudeCLIArgs(claudeRunNameFor("claude-opus-4-6", bare.longContext), "{}", "", false)); got != "claude-opus-4-6" {
		t.Fatalf("bare --model = %q, want claude-opus-4-6", got)
	}
	if got := modelArg(claudeCLIArgs(claudeRunNameFor("claude-opus-4-6", long.longContext), "{}", "", false)); got != "claude-opus-4-6[1m]" {
		t.Fatalf("explicit [1m] --model = %q, want claude-opus-4-6[1m]", got)
	}
	asked, effort := askedAt("claude/claude-opus-4-6:high[1m]")
	if asked != "claude/claude-opus-4-6[1m]" || effort != "high" {
		t.Fatalf("askedAt(claude/claude-opus-4-6:high[1m]) = (%q, %q)", asked, effort)
	}
	if turnKey("claude", bare, bare.Messages) == turnKey("claude", long, long.Messages) {
		t.Fatalf("turnKey collided between bare claude/claude-opus-4-6 and explicit claude/claude-opus-4-6[1m]")
	}
	if newLooseTurn("claude", bare, bare.Messages, false).key == newLooseTurn("claude", long, long.Messages, false).key {
		t.Fatalf("newLooseTurn key collided between bare claude/claude-opus-4-6 and explicit claude/claude-opus-4-6[1m]")
	}
}
