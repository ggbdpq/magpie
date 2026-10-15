package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// nestedKeys is a vendor that answers as each key, and refuses as told
// for each, out of its allowance as ChatGPT says it.
type nestedKeys struct {
	mu    sync.Mutex
	tried []string
	fail  map[string]bool // key → refuse with a 429 out of allowance
}

func (k *nestedKeys) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	k.mu.Lock()
	k.tried = append(k.tried, key)
	refuse := k.fail[key]
	k.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if refuse {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","resets_in_seconds":7200}}`)
		return
	}
	io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"from `+key+`"},"finish_reason":"stop"}]}`)
}

// A nested group's own Affinity holds the requests that reach it through
// the group asked for, as its own firstToken (#2246) and Sink (#2255) do:
// only the group the agent named had its Affinity read, so an inner
// group's strict let the conversation drift to another key.
func TestNestedGroupStrictAffinity(t *testing.T) {
	fresh(t)
	k := &nestedKeys{fail: map[string]bool{}}
	up := httptest.NewServer(k)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "gw", Name: "GW", Key: "k1", Models: []string{"m"}, Chat: up.URL + "/v1",
		Keys: []provider.KeyAccount{{Key: "k2"}}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "inner", Name: "Inner", Members: []string{"gw/m"},
		Affinity: provider.AffinityStrict, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "outer", Name: "Outer", Members: []string{"group/inner"},
		Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	const turn = `{"model":"group/outer","messages":[{"role":"user","content":"hi"}]}`
	if code, body := postAs(t, s, "s1", turn); code != 200 || len(k.tried) != 1 {
		t.Fatalf("first turn: %d %s tried %v", code, body, k.tried)
	}
	first, other := k.tried[0], ""
	if first == "k1" {
		other = "k2"
	} else {
		other = "k1"
	}
	// the key the conversation is on runs out: the inner group's strict
	// keeps the conversation on it anyway, and its error is the agent's —
	// not a move to the other key
	k.mu.Lock()
	k.fail[first] = true
	k.tried = nil
	k.mu.Unlock()
	code, body := postAs(t, s, "s1", turn)
	k.mu.Lock()
	tried := strings.Join(k.tried, ",")
	k.mu.Unlock()
	if code != 429 || tried != first {
		t.Fatalf("second turn, the inner group's strict: %d %s tried %s", code, body, tried)
	}
	a := s.trace.routes[len(s.trace.routes)-1].Affinity
	if a == nil || a.Why != "strict" {
		t.Fatalf("affinity %+v, want strict", a)
	}
	_ = other
}
