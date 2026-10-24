package gateway

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #3453 (hipposonCN): a rule sends every request of a conversation to a
// member whose model refuses the conversation as too long; another member
// takes it, and the next request tries the first all over again — twice
// within one turn of the report's trace. What refused the conversation as
// too long is put behind the member that answered it, for that
// conversation alone: an overflow is the request's length, not the
// account's, so nobody rests — and another conversation, or this one made
// smaller by its agent's compacting, finds the member where the rule put
// it, the size may hold now.
func TestOverflowRefusedOnceIsAskedLast(t *testing.T) {
	fresh(t)
	over := `{"error":{"message":"prompt is too long: 674232 tokens > 200000 token maximum","type":"invalid_request_error"}}`
	a := &scripted{replies: []reply{{400, "", over}, {400, "", over}, {400, "", over}}}
	b := &scripted{replies: []reply{{200, "", chatOK}, {200, "", chatOK}, {200, "", chatOK}}}
	scriptedOn(t, "a", provider.Chat, a)
	scriptedOn(t, "b", provider.Chat, b)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered, Affinity: provider.AffinitySession,
		Rules: []provider.Rule{{Use: "a/m", Tokens: 1}}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	body := `{"model":"group/g","messages":[{"role":"user","content":"say something about this"}]}`

	// the rule puts a first; it refuses the conversation as too long, and
	// b answers
	code, out := postAs(t, s, "s1", body)
	if code != 200 || !strings.Contains(out, "hello") || a.n != 1 || b.n != 1 {
		t.Fatalf("first: %d %s (a %d, b %d)", code, out, a.n, b.n)
	}
	r := lastRoute(s)
	if len(r.Tries) != 2 || r.Tries[0].Fail != failOverflow {
		t.Fatalf("first tries: %+v", r.Tries)
	}

	// the next request of the same conversation, the rule heard as it
	// was: b is asked first, what a refused as too long at this size it
	// refuses again — the conversation has only grown
	a.n, b.n = 0, 0
	code, out = postAs(t, s, "s1", body)
	if code != 200 || a.n != 0 || b.n != 1 {
		t.Fatalf("second: %d %s (a %d, b %d)", code, out, a.n, b.n)
	}

	// another conversation begins with a, as the rule says: what a
	// refused was the first conversation's length, not this one's
	a.n, b.n = 0, 0
	code, out = postAs(t, s, "s2", body)
	if code != 200 || a.n != 1 || b.n != 1 {
		t.Fatalf("another conversation: %d %s (a %d, b %d)", code, out, a.n, b.n)
	}

	// the first conversation compacted to less than what a refused: a is
	// asked as the rule says again, the size may hold now
	a.n, b.n = 0, 0
	code, out = postAs(t, s, "s1", `{"model":"group/g","messages":[{"role":"user","content":"up"}]}`)
	if code != 200 || a.n != 1 || b.n != 1 {
		t.Fatalf("compacted: %d %s (a %d, b %d)", code, out, a.n, b.n)
	}
}
