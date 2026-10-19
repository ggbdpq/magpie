package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A try magpie itself turned away — its key's or account's queue full
// (QueueLimit), or one that waited QueueWait for a slot — is told to the
// agent as a 429 exactly as a vendor's rate limit is, and its routing
// trace try now says whose it was: fail "local", where a vendor's 429
// carries what its body called the failure (fail "quota", "rate" …) or,
// on the last try, nothing at all. The agent's answer is unchanged
// (#2939: a day's routing log held 7,761 429s, 18 of them the vendor's).
func TestLocalTurnAwayIsMarkedInItsTry(t *testing.T) {
	fresh(t)
	v, up := slowUp(t)
	one := 1
	if err := provider.Save(provider.Provider{ID: "slow", Name: "Slow", Key: "k", Models: []string{"m"}, Chat: up + "/v1",
		MaxConcurrency: &one, QueueLimit: 1, QueueWait: 1}); err != nil {
		t.Fatal(err)
	}
	g := newLaneRig(t)
	lane := func() Lane { return g.s.Lanes()["slow#"+provider.KeyID("k")] }

	g.send(context.Background(), "/v1/chat/completions", "slow/m", "a")
	within(t, "a at the vendor", func() bool { return len(v.seen()) == 1 })
	g.send(context.Background(), "/v1/chat/completions", "slow/m", "b")
	within(t, "b queued", func() bool { return lane().Waiting == 1 })

	// the queue full: c turned away at once, the agent's 429 as it has
	// always been — OpenAI's shape, its type, Retry-After — and the
	// vendor never asked
	g.send(context.Background(), "/v1/chat/completions", "slow/m", "c")
	r := g.reply()
	if r.tag != "c" || r.status != 429 || r.header.Get("Retry-After") == "" {
		t.Fatalf("c = %+v, want the usual immediate 429 with Retry-After", r)
	}
	var oe struct {
		Error struct {
			Message, Type string
		} `json:"error"`
	}
	if json.Unmarshal([]byte(r.body), &oe) != nil || oe.Error.Type == "" || !strings.Contains(oe.Error.Message, "queue is full") {
		t.Fatalf("c's body = %s, want the same OpenAI-shaped 429 as ever", r.body)
	}

	// b waited its QueueWait and is turned away too
	r = g.reply()
	if r.tag != "b" || r.status != 429 || !strings.Contains(r.body, "waited") {
		t.Fatalf("b = %+v, want the usual 429 for waiting too long", r)
	}

	// the routing trace says both tries were magpie's own turn-aways,
	// not the vendor's: fail "local" on each, the 429 they carry unchanged
	marked := 0
	for _, rt := range g.s.Trace(context.Background(), 0, 0).Routes {
		for _, tr := range rt.Tries {
			if tr.Status != 429 {
				continue
			}
			if strings.Contains(tr.Error, "queue is full") || strings.Contains(tr.Error, "waited") {
				if tr.Fail != "local" {
					t.Fatalf("try %q (%s) = fail %q, want \"local\"", tr.ID, tr.Error, tr.Fail)
				}
				marked++
			}
		}
	}
	if marked != 2 {
		t.Fatalf("%d turn-away tries in the trace, want 2", marked)
	}

	close(v.gate("a"))
	if r := g.reply(); r.tag != "a" || !strings.Contains(r.body, "[DONE]") {
		t.Fatalf("a = %+v", r)
	}
	within(t, "every slot given back", func() bool { return len(g.s.Lanes()) == 0 })
}
