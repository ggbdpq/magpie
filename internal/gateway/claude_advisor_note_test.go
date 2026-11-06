package gateway

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A turn that declares Claude Code's advisor on the subscription line says
// so in magpie's log: the run magpie drives is not started with the
// advisor, so the tool is left out of the request and the model is told it
// is gone. Said by nobody, the user only saw a model that never calls the
// advisor, on a 200 (#5514).
func TestClaudeSubscriptionSaysWhenTheAdvisorIsLeftOut(t *testing.T) {
	s := New()
	t.Cleanup(s.subscription.abortAll)
	body := []byte(`{"model":"claude-sonnet-5","max_tokens":64,` +
		`"tools":[{"type":"advisor_20260301","name":"advisor","model":"claude-opus-5-5"}],` +
		`"messages":[{"role":"user","content":"Before answering, call the advisor tool once to ask: is 2+2=4? Then reply in one word."}]}`)
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(old) })
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	s.serveSubscription(w, r, provider.Anthropic, "Claude Code", "claude-sonnet-5", "test", body, &Usage{},
		func(context.Context, *Request) (*subscriptionRun, <-chan Event, error) {
			return nil, nil, fmt.Errorf("no run is started; the note is what the test reads")
		})
	if !strings.Contains(logs.String(), "declares Claude Code's advisor") {
		t.Fatalf("the advisor left out was not said: %q", logs.String())
	}
}

// A turn that declares no advisor says nothing: the note is for the advisor
// alone.
func TestClaudeSubscriptionWithoutAnAdvisorSaysNothing(t *testing.T) {
	s := New()
	t.Cleanup(s.subscription.abortAll)
	body := []byte(`{"model":"claude-sonnet-5","max_tokens":64,` +
		`"tools":[{"name":"read","input_schema":{"type":"object"}}],` +
		`"messages":[{"role":"user","content":"hi"}]}`)
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(old) })
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	s.serveSubscription(w, r, provider.Anthropic, "Claude Code", "claude-sonnet-5", "test", body, &Usage{},
		func(context.Context, *Request) (*subscriptionRun, <-chan Event, error) {
			return nil, nil, fmt.Errorf("no run is started")
		})
	if strings.Contains(logs.String(), "advisor") {
		t.Fatalf("a turn with no advisor was noted: %q", logs.String())
	}
}
