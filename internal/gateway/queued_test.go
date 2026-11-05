package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// The Qoder plugin (0.2.40, #4049) answers a queued request at once with
// a 503 + Retry-After that names the queue, when magpie has another
// candidate to ask. Every member's answer says its queue, so asking them
// again as a group's patience does only queues the same request again, on
// every account, one whole round after another (#5346: 217 accounts,
// tries up to 656 for one request, 20 GB of re-uploaded bodies a day).
// A queue isn't a trouble a pause mends: each member is asked once, and
// the last one left waits its Retry-After as the vendor's client does.
func TestQueuedGroupAsksEachMemberOnce(t *testing.T) {
	const queued = `{"error":{"message":"the model is busy, the request is queued at position 12","code":10605}}`
	keys := 3
	u := &busyUpstream{replies: []busyReply{{code: 503, retryAfter: "0.04", body: queued}}}
	fresh(t)
	up := httptest.NewServer(u)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "a", Name: "A", Key: "k0", Models: []string{"m"}, Chat: up.URL + "/v1", Routing: "order", Patience: provider.PatienceDefault}
	for i := 1; i < keys; i++ {
		p.Keys = append(p.Keys, provider.KeyAccount{Key: string(rune('0' + i))})
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	s := New()
	scaled(t, 3*time.Second)
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)
	res, err := http.Post(gw.URL+"/v1/chat/completions", "application/json", strings.NewReader(singleAsk(false)))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.StatusCode != 503 {
		t.Fatalf("the agent was told %d, want the 503", res.StatusCode)
	}
	// two keys give way, the last waits its Retry-After and is asked again
	// twice, as the vendor's client is
	if n := u.asked(); n > keys+lastRetries {
		t.Fatalf("a queued group asked %d times, want %d at most (each key once, the last one's same-one retries)", n, keys+lastRetries)
	}
}

// A 5xx that says nothing of a queue is still one a group's patience
// waits out, the members asked again in turn (#1418): the queue's words,
// not the 503, are what end the rounds.
func TestOverloadedGroupStillReplans(t *testing.T) {
	const overloaded = `{"error":{"message":"upstream overloaded, please try again","type":"api_error"}}`
	keys := 3
	u := &busyUpstream{replies: []busyReply{{code: 503, retryAfter: "0.04", body: overloaded}}}
	fresh(t)
	up := httptest.NewServer(u)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "a", Name: "A", Key: "k0", Models: []string{"m"}, Chat: up.URL + "/v1", Routing: "order", Patience: provider.PatienceDefault}
	for i := 1; i < keys; i++ {
		p.Keys = append(p.Keys, provider.KeyAccount{Key: string(rune('0' + i))})
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	s := New()
	scaled(t, 3*time.Second)
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)
	res, err := http.Post(gw.URL+"/v1/chat/completions", "application/json", strings.NewReader(singleAsk(false)))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if n := u.asked(); n <= keys+lastRetries {
		t.Fatalf("an overloaded group asked %d times, want more than %d: its patience asks the members again", n, keys+lastRetries)
	}
}
