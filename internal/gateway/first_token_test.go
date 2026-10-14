package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// slowStart streams the role chunk at once and its text only after wait,
// or never when the request goes first.
type slowStart struct {
	wait time.Duration
	n    int
}

func (s *slowStart) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	s.n++
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	io.WriteString(w, `data: {"id":"s","choices":[{"index":0,"delta":{"role":"assistant"}}]}`+"\n\n")
	w.(http.Flusher).Flush()
	select {
	case <-r.Context().Done():
		return
	case <-time.After(s.wait):
	}
	io.WriteString(w, `data: {"id":"s","choices":[{"index":0,"delta":{"content":"slow answer"},"finish_reason":"stop"}]}`+"\n\n"+"data: [DONE]\n\n")
}

const chatStreamOK = `data: {"id":"b","choices":[{"index":0,"delta":{"role":"assistant","content":"from b"},"finish_reason":"stop"}]}` + "\n\n" + "data: [DONE]\n\n"

func slowOn(t *testing.T, id string, h http.Handler) {
	t.Helper()
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: id, Name: strings.ToUpper(id), Key: "k", Models: []string{"m"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
}

// A member that hasn't begun its answer within the group's first-token
// time is let go before any of it reaches the agent, and the next one
// answers; the slow one doesn't rest. The last one is always waited for.
func TestGroupFirstTokenFailsOver(t *testing.T) {
	fresh(t)
	a := &slowStart{wait: 10 * time.Second}
	b := &scripted{replies: []reply{{200, "text/event-stream", chatStreamOK}}}
	slowOn(t, "a", a)
	scriptedOn(t, "b", provider.Chat, b)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered, FirstToken: 1}); err != nil {
		t.Fatal(err)
	}
	s := New()
	began := time.Now()
	code, body := postAs(t, s, "", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if took := time.Since(began); took > 5*time.Second {
		t.Fatalf("took %s", took)
	}
	if code != 200 || !strings.Contains(body, "from b") || strings.Contains(body, "slow answer") || a.n != 1 || b.n != 1 {
		t.Fatalf("%d %s (a %d, b %d)", code, body, a.n, b.n)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 2 || r.Tries[0].Fail != failSlow || r.Tries[0].Rest != nil || r.Tries[1].Status != 200 {
		t.Fatalf("tries: %+v", r.Tries)
	}
	recs := usage.Load(time.Time{})
	if len(recs) == 0 {
		t.Fatal("no record")
	}
	last := recs[len(recs)-1]
	if last.Sent <= 0 || last.TTFT <= 0 || last.Sent > last.TTFT {
		t.Fatalf("sent %d, ttft %d", last.Sent, last.TTFT)
	}

	// the last one left is waited for
	fresh(t)
	a = &slowStart{wait: 2 * time.Second}
	slowOn(t, "a", a)
	provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m"}, Routing: provider.Ordered, FirstToken: 1})
	s = New()
	if code, body := postAs(t, s, "", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`); code != 200 || !strings.Contains(body, "slow answer") {
		t.Fatalf("last: %d %s", code, body)
	}

	// a member that answers in time is kept
	fresh(t)
	a = &slowStart{wait: 100 * time.Millisecond}
	b = &scripted{replies: []reply{{200, "text/event-stream", chatStreamOK}}}
	slowOn(t, "a", a)
	scriptedOn(t, "b", provider.Chat, b)
	provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered, FirstToken: 1})
	s = New()
	if code, body := postAs(t, s, "", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`); code != 200 || !strings.Contains(body, "slow answer") || b.n != 0 {
		t.Fatalf("in time: %d %s (b %d)", code, body, b.n)
	}
}

// A member reached through a group in the group is waited for by that
// group's first-token time, not the one the agent asked for: "how long
// this group's members may take to start" is what the field on the group
// says, and the seat belongs to the group it is spelled through (#2246).
// A group in the group that names none leaves it to the outer one.
func TestNestedGroupFirstToken(t *testing.T) {
	// slow past the outer's time, within its own group's: waited for
	fresh(t)
	a := &slowStart{wait: 2500 * time.Millisecond}
	b := &scripted{replies: []reply{{200, "text/event-stream", chatStreamOK}}}
	slowOn(t, "a", a)
	scriptedOn(t, "b", provider.Chat, b)
	if err := provider.SaveGroup(provider.Group{Name: "S", Members: []string{"a/m"}, Routing: provider.Ordered, FirstToken: 4}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"group/s", "b/m"}, Routing: provider.Ordered, FirstToken: 1}); err != nil {
		t.Fatal(err)
	}
	s := New()
	began := time.Now()
	code, body := postAs(t, s, "", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if took := time.Since(began); took > 3500*time.Millisecond {
		t.Fatalf("took %s", took)
	}
	if code != 200 || !strings.Contains(body, "slow answer") || strings.Contains(body, "from b") || b.n != 0 {
		t.Fatalf("%d %s (a %d, b %d)", code, body, a.n, b.n)
	}

	// the group in the group naming none, the outer's time cuts
	fresh(t)
	a = &slowStart{wait: 10 * time.Second}
	b = &scripted{replies: []reply{{200, "text/event-stream", chatStreamOK}}}
	slowOn(t, "a", a)
	scriptedOn(t, "b", provider.Chat, b)
	provider.SaveGroup(provider.Group{Name: "S", Members: []string{"a/m"}, Routing: provider.Ordered})
	provider.SaveGroup(provider.Group{Name: "G", Members: []string{"group/s", "b/m"}, Routing: provider.Ordered, FirstToken: 1})
	s = New()
	if code, body := postAs(t, s, "", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`); code != 200 || !strings.Contains(body, "from b") || a.n != 1 {
		t.Fatalf("inner unset: %d %s (a %d)", code, body, a.n)
	}

	// and the seconds the seat is given up at are its own group's
	fresh(t)
	a = &slowStart{wait: 10 * time.Second}
	b = &scripted{replies: []reply{{200, "text/event-stream", chatStreamOK}}}
	slowOn(t, "a", a)
	scriptedOn(t, "b", provider.Chat, b)
	provider.SaveGroup(provider.Group{Name: "S", Members: []string{"a/m"}, Routing: provider.Ordered, FirstToken: 1})
	provider.SaveGroup(provider.Group{Name: "G", Members: []string{"group/s", "b/m"}, Routing: provider.Ordered, FirstToken: 2})
	s = New()
	postAs(t, s, "", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if r := lastRoute(s); len(r.Tries) == 0 || !strings.Contains(r.Tries[0].Error, "no first token in 1s") {
		t.Fatalf("cut: tries[0] %+v", r.Tries[0])
	}

	// a group in the way naming one, a group below it naming none: the
	// nearest one above the seat that named it holds
	fresh(t)
	a = &slowStart{wait: 10 * time.Second}
	b = &scripted{replies: []reply{{200, "text/event-stream", chatStreamOK}}}
	slowOn(t, "a", a)
	scriptedOn(t, "b", provider.Chat, b)
	provider.SaveGroup(provider.Group{Name: "T", Members: []string{"a/m"}, Routing: provider.Ordered})
	provider.SaveGroup(provider.Group{Name: "S", Members: []string{"group/t"}, Routing: provider.Ordered, FirstToken: 1})
	provider.SaveGroup(provider.Group{Name: "G", Members: []string{"group/s", "b/m"}, Routing: provider.Ordered, FirstToken: 2})
	s = New()
	postAs(t, s, "", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if r := lastRoute(s); len(r.Tries) == 0 || !strings.Contains(r.Tries[0].Error, "no first token in 1s") {
		t.Fatalf("inherited: tries[0] %+v", r.Tries[0])
	}
}
