package gateway

import (
	"encoding/json"
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

// deadClaude stands in for Claude Code whose every process dies as soon as
// it has said its reply's tool calls: a crash, an update, the machine's OOM
// killer. What it said is on its output, what its caller got back is whole.
// It keeps its session as Claude Code does, a file under its config
// folder's project for the folder it works in, the one --resume names when
// it is given one. Each process's arguments and the lines it was told go to
// its own log.
func deadClaude(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("CLAUDE_CODE_TMPDIR", t.TempDir())
	config := filepath.Join(home, ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	logs := t.TempDir()
	dir := t.TempDir()
	script := `#!/bin/sh
log=` + logs + `/$$
echo "args $*" > $log
sid=s$$
prev=
for a in "$@"; do
  if [ "$prev" = "--resume" ]; then sid=$a; fi
  prev=$a
done
proj="$CLAUDE_CONFIG_DIR/projects/$(pwd -P | sed 's/[^A-Za-z0-9]/-/g')"
mkdir -p "$proj"
file="$proj/$sid.jsonl"
echo '{"type":"system","subtype":"init","session_id":"'$sid'"}'
while read -r line; do
  case $line in *'"type":"control_request"'*)
    id=$(printf '%s' "$line" | sed 's/.*"request_id":"\([^"]*\)".*/\1/')
    echo '{"type":"control_response","response":{"subtype":"success","request_id":"'$id'"}}'
    continue ;;
  esac
  echo "told $line" >> $log
  printf '%s\n' "$line" >> "$file"
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-sonnet-5","usage":{"input_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text"}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"reading"}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_stop","index":0}}'
  echo '{"type":"stream_event","event":{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"mcp__magpie__read"}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{}"}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_stop","index":1}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  exit 7
done
`
	testenv.Program(t, filepath.Join(dir, "claude"), script)
	t.Setenv("PATH", dir+string(filepath.ListSeparator)+os.Getenv("PATH"))
	return logs
}

// askDead sends body as a Claude subscription request and says the reply's
// first words.
func askDead(t *testing.T, s *Server, body string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	var u Usage
	p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
	if code, msg := s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, p, "claude-sonnet-5", []byte(body), &u); code != 200 {
		t.Fatalf("%d %s", code, msg)
	}
	var res struct {
		Content []struct{ Text string } `json:"content"`
	}
	json.Unmarshal(rec.Body.Bytes(), &res)
	if len(res.Content) == 0 {
		t.Fatalf("no answer: %s", rec.Body)
	}
	return res.Content[0].Text
}

// bodyOf turns messages into a request body offering read.
func bodyOf(t *testing.T, msgs string) string {
	t.Helper()
	return `{"model":"claude-sonnet-5","max_tokens":100,"system":"be brief","tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":[` + msgs + `]}`
}

// logsOf is each process's log, the arguments it started with first.
func logsOf(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		out[e.Name()] = string(b)
	}
	return out
}

// #4863: a run parked on its reply's tool calls dies — its Claude Code
// exits, or the oversized budget lets it go — and its session file went
// with it (removeRun): no run waits, and nothing is on the shelf where the
// results' request comes by. A client whose turn the results end — a
// compaction among them (pi compacts mid-conversation) — is told the whole
// conversation in one message, a prefix Anthropic has never cached, and
// writes all of it again. The run's session is now shelved where the
// results arrive, and that turn goes on from it, told the results alone.
func TestClaudeRunDeadWhileParkedLeavesItsSession(t *testing.T) {
	logs := deadClaude(t)
	s := New()
	t.Cleanup(s.subscription.abortAll)

	// the turn's reply calls the client's read tool, and its Claude Code
	// dies saying it
	first := askDead(t, s, bodyOf(t, rmsg("user", "read the file")))
	if first == "" {
		t.Fatal("no answer")
	}
	for deadline := time.Now().Add(10 * time.Second); ; {
		s.subscription.mu.Lock()
		n := len(s.subscription.runs)
		s.subscription.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the run that died on its calls was never taken off the bridge")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// the results come with the whole conversation, as a client sends them
	msgs := rmsg("user", "read the file") + `,` + rmsg("assistant", first) +
		`,{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"read","input":{}}]}` +
		`,{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"the file says hello"}]}`
	before := logsOf(t, logs)
	again := askDead(t, s, bodyOf(t, msgs))
	if again == "" {
		t.Fatal("no answer to the results")
	}
	after := logsOf(t, logs)
	for p, l := range after {
		if _, ok := before[p]; ok {
			continue
		}
		// the run that went on with the results resumed the dead run's
		// session, told the results alone; told whole, the conversation
		// was one message with the first turn in it
		if !strings.Contains(l, "--resume ") {
			t.Fatalf("the results went to a new Claude Code told the whole conversation:\n%s", l)
		}
		told := l[strings.LastIndex(l, "\ntold "):]
		if !strings.Contains(told, "the file says hello") || strings.Contains(told, "read the file") {
			t.Fatalf("the run that went on with the results was told other than the results:\n%s", told)
		}
		return
	}
	t.Fatalf("no new run answered the results: %v", after)
}
