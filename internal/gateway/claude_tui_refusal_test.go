package gateway

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/claudetui"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// A request the interactive mode refuses is refused the same on every
// account: it is a 400 no account rests for and the next isn't asked,
// where a 502 was asked of every account in turn and rested some (#4778).
func TestClaudeTUIRefusalIsNoAccountsFault(t *testing.T) {
	h, _ := tuiHelperHarness(t, "")
	body := `{"model":"claude-sonnet-5","max_tokens":100,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"http://127.0.0.1:1/a.png"}},{"type":"text","text":"what is it?"}]}]}`
	var u Usage
	rec := httptest.NewRecorder()
	code, msg := h.s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, h.p, "claude-sonnet-5", []byte(body), &u)
	if code != 400 || !strings.Contains(msg, "couldn't fetch the image") {
		t.Fatalf("%d %.300s", code, msg)
	}
	if retryable(code, rec.Body.Bytes()) {
		t.Fatalf("the refusal is asked of the next account: %s", rec.Body.String())
	}
}

// tuiThirdPartyRefusal is the 400 an interactive Claude Code answers a
// turn whose prompt is another agent harness's own (#4503).
const tuiThirdPartyRefusal = `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"API Error: 400 Third-party apps now draw from your extra usage, not your plan limits. Add more at claude.ai/settings/usage and keep going.","api_error_status":400}`

// tuiThirdPartyHarness is tuiHelperHarness whose interactive Claude Code
// answers a turn the way Anthropic refuses one told through it when its
// prompt is another agent harness's own (#4503): a 400 that says "keep
// going" — and claude -p, which the helper is not, serves the same
// request as its own entrypoint does. As -p the fake answers print: the
// shell lines of an answer of its own, or the usual "ok" when print is "".
func tuiThirdPartyHarness(t *testing.T, print string) (*resumeHarness, string) {
	t.Helper()
	start := `if [ -n "$MG_TEST_TUI" ]; then
  echo '{"type":"system","subtype":"init","session_id":"'$sid'"}'
  while read -r line; do
    case $line in *'"type":"control_request"'*)
      id=$(printf '%s' "$line" | sed 's/.*"request_id":"\([^"]*\)".*/\1/')
      echo '{"type":"control_response","response":{"subtype":"success","request_id":"'$id'"}}'
      continue ;;
    esac
    echo '` + tuiThirdPartyRefusal + `'
    exit 1
  done
  exit 0
fi
` + print
	h := newScriptHarness(t, "ok", start)
	claudeTUIRuns = true
	t.Cleanup(func() { claudeTUIRuns = false })
	started := filepath.Join(t.TempDir(), "started")
	helper := filepath.Join(t.TempDir(), "magpie")
	testenv.Program(t, helper, `#!/bin/sh
[ "$1" = "`+claudetui.Command+`" ] || exit 9
echo start >> '`+started+`'
MG_TEST_TUI=1
export MG_TEST_TUI
shift; claude=$1; shift
exec "$claude" "$@"
`)
	claudeTUIHelper = helper
	t.Cleanup(func() { claudeTUIHelper = "" })
	return h, started
}

// An interactive Claude Code told a caller agent's own system prompt is
// refused by Anthropic as a third-party app's, a 400 that says "keep
// going" (#4503): the notice is no answer to give the caller, and claude
// -p serves the same request, its entrypoint not judged by the prompt.
// The turn is redone once as claude -p, and the client has its reply,
// where the 400 reached it whole before. The interactive mode is tried
// first still: only the turn Anthropic refuses goes the other way.
func TestClaudeTUIThirdPartyRefusalRedoneAsPrint(t *testing.T) {
	h, started := tuiThirdPartyHarness(t, "")
	code, msg := h.askCode("an agent harness's own prompt", "hi")
	if code != 200 {
		t.Fatalf("%d %.300s", code, msg)
	}
	if n := helperStarts(started); n != 1 {
		t.Fatalf("the helper started %d times, want once: the redo is claude -p, which no helper starts", n)
	}
}

// The redo is once: claude -p too refusing the turn the same way, the
// client has that refusal, not a third run — a -p run is not redone
// (#4503).
func TestClaudeTUIThirdPartyRefusalRedoneOnce(t *testing.T) {
	print := `echo '{"type":"system","subtype":"init","session_id":"'$sid'"}'
while read -r line; do
  case $line in *'"type":"control_request"'*) continue ;; esac
  echo '` + tuiThirdPartyRefusal + `'
  exit 1
done
exit 0
`
	h, started := tuiThirdPartyHarness(t, print)
	code, msg := h.askCode("an agent harness's own prompt", "hi")
	if code != 400 || !strings.Contains(msg, "Third-party apps now draw from your extra usage") {
		t.Fatalf("%d %.300s", code, msg)
	}
	if n := helperStarts(started); n != 1 {
		t.Fatalf("the helper started %d times, want once", n)
	}
	if runs := h.runs(); len(runs) != 2 {
		t.Fatalf("%d runs, want two: the interactive mode's, then the redo as claude -p", len(runs))
	}
}
