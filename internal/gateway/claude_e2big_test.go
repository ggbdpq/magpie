package gateway

import (
	"io/fs"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/provider"
)

// fakeE2BIG stands for a kernel refusing an exec for size: one of the
// strings the run would carry is longer than one exec string may be
// (E2BIG, "argument list too long", #4978). refuse says which commands it
// refuses; the others start as they would.
func fakeE2BIG(t *testing.T, refuse func(cmd *exec.Cmd) bool) {
	t.Helper()
	old := startTree
	startTree = func(cmd *exec.Cmd) (*proc.Tree, error) {
		if refuse(cmd) {
			return nil, &fs.PathError{Op: "fork/exec", Path: cmd.Path, Err: syscall.E2BIG}
		}
		return proc.StartTree(cmd)
	}
	t.Cleanup(func() { startTree = old })
}

// The interactive mode's Claude Code is given its safeguards in one
// environment entry (CLAUDE_CODE_EXTRA_BODY), read once as it starts. In
// a long auto-mode conversation the classifier context the client sends
// grows turn by turn (#4804), and where one exec string carries no more
// than the kernel allows, the exec of the helper failed "argument list
// too long" for magpie's own binary: the turn was answered 502, Claude
// Code asked again, and every try failed the same way, so the
// conversation went on nowhere (#4978). Such a turn is answered as
// claude -p, whose safeguards go over stdin, and the interactive mode is
// tried first again on the next turn.
func TestClaudeTUIEnvTooBigRunsAsPrint(t *testing.T) {
	h := tuiScriptHarness(t, `echo "extra ${CLAUDE_CODE_EXTRA_BODY-unset}" >> $log`)
	fakeE2BIG(t, func(cmd *exec.Cmd) bool { return cmd.Path == claudeTUIHelper })
	h.extra = safeguards
	code, msg := h.askCode("rules", "hi")
	if code != 200 {
		t.Fatalf("the turn whose interactive exec was refused was not answered: %d %s", code, msg)
	}
	runs := h.runs()
	if len(runs) != 1 {
		t.Fatalf("%d runs started, want the one claude -p answered: %v", len(runs), runs)
	}
	var log string
	for _, said := range runs {
		log = said
	}
	if !strings.Contains(log, "apply_flag_settings") || !strings.Contains(log, "classifier_context") {
		t.Fatalf("the print run's safeguards didn't go over stdin: %s", log)
	}
	if !strings.Contains(log, "extra unset") {
		t.Fatalf("the print run was given the safeguards in an environment entry: %s", log)
	}
}

// Beside the interactive mode's safeguards, one exec string of a claude
// -p run can grow with what a client's request asks: --json-schema, the
// answer's schema. The exec refused over it is the request's 400, its
// size said, where the E2BIG was a 502 every account was asked in turn
// and rested some (#4778, #4978).
func TestClaudePrintSchemaTooBigRefused(t *testing.T) {
	h := newScriptHarness(t, "ok", "")
	fakeE2BIG(t, func(*exec.Cmd) bool { return true })
	schema := `{"type":"object","properties":{"title":{"type":"string"}}}`
	h.extra = `"output_config":{"format":{"type":"json_schema","schema":` + schema + `}},`
	body := `{"model":"claude-sonnet-5","max_tokens":100,` + h.extra + `"system":"rules","tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`
	var u Usage
	rec := httptest.NewRecorder()
	code, msg := h.s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, h.p, "claude-sonnet-5", []byte(body), &u)
	if code != 400 {
		t.Fatalf("a schema too big for one exec argument was not refused: %d %s", code, msg)
	}
	if !strings.Contains(msg, "schema") || !strings.Contains(msg, strconv.Itoa(len(schema))) {
		t.Fatalf("the refusal doesn't say the schema's size: %s", msg)
	}
	if retryable(code, rec.Body.Bytes()) {
		t.Fatalf("the refusal is asked of the next account: %s", rec.Body.String())
	}
	if runs := h.runs(); len(runs) != 0 {
		t.Fatalf("%d runs were started for a request refused before its exec", len(runs))
	}
}
