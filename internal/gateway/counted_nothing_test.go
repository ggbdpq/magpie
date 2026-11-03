package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A 200 whose body is an upstream billing notice and whose usage counts
// nothing is no answer (yetone/magpie#3967): nothing was generated, so
// the body is not the completion the agent asked for, and the try fails
// as its words would have with their own status — a billing notice as the
// 402 it owes — for the next member to answer. Reported and all zero is
// what reads; a reply that never reports a usage stays an answer, as one
// that ignores stream_options' include_usage does.
const billingNotice = "Your subscription has expired. API key status: expired. USDT balance: 12.5 USDT."

const noticeJSON = `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"` + billingNotice + `"},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`

const countedJSON = `{"id":"c2","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}}`

var noticeSSE = sse(`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"`+billingNotice+`"}}]}`,
	`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`,
	`data: [DONE]`)

var countedSSE = sse(`data: {"id":"c2","choices":[{"index":0,"delta":{"content":"from b"}}]}`,
	`data: {"id":"c2","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	`data: {"id":"c2","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}}`,
	`data: [DONE]`)

// The body reached the client as a completion through a relayed request,
// where the reply comes as it came: the try fails, and the next member
// answers in the turn.
func TestCountedNothingFailsOver(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{200, "", noticeJSON}}}
	b := &scripted{replies: []reply{{200, "", countedJSON}}}
	scriptedOn(t, "a", provider.Chat, a)
	scriptedOn(t, "b", provider.Chat, b)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "hello") || strings.Contains(body, "subscription") {
		t.Fatalf("%d %s %+v", code, body, s.trace.routes[len(s.trace.routes)-1])
	}
	if a.n != 1 || b.n != 1 {
		t.Fatalf("a asked %d, b asked %d", a.n, b.n)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 2 || r.Tries[0].Fail == "" {
		t.Fatalf("tries: %+v", r.Tries)
	}
}

// The same through a translation, where magpie reads the reply itself:
// a reply that reported its usage as counting nothing is not an answer,
// and what it said (a billing notice here) never reaches the agent.
func TestCountedNothingTranslatedFailsOver(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{200, "text/event-stream", noticeSSE}}}
	b := &scripted{replies: []reply{{200, "text/event-stream", countedSSE}}}
	scriptedOn(t, "a", provider.Chat, a)
	scriptedOn(t, "b", provider.Chat, b)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"group/g","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)))
	code, body := rec.Code, rec.Body.String()
	if code != 200 || !strings.Contains(body, "from b") || strings.Contains(body, "subscription") {
		t.Fatalf("%d %s %+v", code, body, s.trace.routes[len(s.trace.routes)-1])
	}
	if a.n != 1 || b.n != 1 {
		t.Fatalf("a asked %d, b asked %d", a.n, b.n)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 2 || r.Tries[0].Fail == "" {
		t.Fatalf("tries: %+v", r.Tries)
	}
}

// A streamed turn cannot be taken back once its text has passed, so the
// first turn still carries the notice; what the failure buys is the seat
// set aside and the conversation's stick dropped, so the next turn of the
// session is answered by another member instead of the same notice again.
func TestCountedNothingStreamDropsTheStick(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{200, "text/event-stream", noticeSSE}}}
	b := &scripted{replies: []reply{{200, "text/event-stream", countedSSE}}}
	scriptedOn(t, "a", provider.Chat, a)
	scriptedOn(t, "b", provider.Chat, b)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	send := func() (int, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions",
			strings.NewReader(`{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("x-session-id", "s1")
		s.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	code, _ := send()
	r := s.trace.routes[len(s.trace.routes)-1]
	if code != 200 || len(r.Tries) != 1 || r.Tries[0].Fail == "" || r.Tries[0].Rest == nil {
		t.Fatalf("turn1 %d fails %+v", code, r.Tries)
	}
	code, body := send()
	if code != 200 || !strings.Contains(body, "from b") || strings.Contains(body, "subscription") {
		t.Fatalf("turn2 %d %s %+v", code, body, s.trace.routes[len(s.trace.routes)-1])
	}
	if a.n != 1 || b.n != 1 {
		t.Fatalf("a asked %d, b asked %d across the two turns", a.n, b.n)
	}
}

// The same streamed turn through a translation, where magpie reads the
// events itself: the notice leaks as the first turn's text all the same,
// and the failure is booked, the seat rested, and the stick dropped for
// the session's next turn to be answered elsewhere.
func TestCountedNothingTranslatedStreamDropsTheStick(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{200, "text/event-stream", noticeSSE}}}
	b := &scripted{replies: []reply{{200, "text/event-stream", countedSSE}}}
	scriptedOn(t, "a", provider.Chat, a)
	scriptedOn(t, "b", provider.Chat, b)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	send := func() (int, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/messages",
			strings.NewReader(`{"model":"group/g","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("x-session-id", "s1")
		s.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	code, _ := send()
	r := s.trace.routes[len(s.trace.routes)-1]
	if code != 200 || len(r.Tries) != 1 || r.Tries[0].Fail == "" || r.Tries[0].Rest == nil {
		t.Fatalf("turn1 %d fails %+v", code, r.Tries)
	}
	code, body := send()
	if code != 200 || !strings.Contains(body, "from b") || strings.Contains(body, "subscription") {
		t.Fatalf("turn2 %d %s %+v", code, body, s.trace.routes[len(s.trace.routes)-1])
	}
	if a.n != 1 || b.n != 1 {
		t.Fatalf("a asked %d, b asked %d across the two turns", a.n, b.n)
	}
}

// The exempt class: a reply that never reports a usage — one that ignores
// stream_options' include_usage — stays the answer it is, though nothing
// is counted of it.
func TestUsageNeverReportedStaysAnAnswer(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{200, "", chatOK}}}
	scriptedOn(t, "a", provider.Chat, a)
	s := New()
	if code, body := postAs(t, s, "", `{"model":"a/m","messages":[{"role":"user","content":"hi"}]}`); code != 200 || !strings.Contains(body, "hello") {
		t.Fatalf("%d %s", code, body)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 1 || r.Tries[0].Fail != "" {
		t.Fatalf("tries: %+v", r.Tries)
	}

	fresh(t)
	a = &scripted{replies: []reply{{200, "text/event-stream", sse(`data: {"id":"c3","choices":[{"index":0,"delta":{"content":"said anyway"}}]}`,
		`data: {"id":"c3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}}}
	scriptedOn(t, "a", provider.Chat, a)
	s = New()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"a/m","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "said anyway") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	r = s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 1 || r.Tries[0].Fail != "" {
		t.Fatalf("tries: %+v", r.Tries)
	}
}
