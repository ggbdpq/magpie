package gateway

// The affine half of a keeping pool (#4030): while the conversation's own
// account rests out of a short 429 its pool keeps through, the seat holds
// (affine says "keep", not "resting"), the ready one answers without
// taking the conversation over, and once the rest is over the next turn
// goes back to the account that has its cache. End to end: two turns, the
// second refused with a short 429 and answered by the pool's other key,
// a turn in between still on the ready one, and the turn after the rest
// back on the first key.
import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A keeping pool's seat resting out of a rate limit: affine says "keep",
// keeps it, and leaves the ready one in front — the seat doesn't take
// the request while it rests. A pool without the setting says "resting",
// unkept, as before.
func TestPoolKeepHoldsTheSeatThroughARest(t *testing.T) {
	for _, keep := range []bool{true, false} {
		p := provider.Provider{ID: "kimi", KeepThroughRest: keep}
		seat := candidate{p: p, model: "m", rest: "kimi@one"}
		ready := candidate{p: p, model: "m", rest: "kimi@two"}
		restAt(t, "kimi@one", failRate)
		cs, pl := restLast([]candidate{seat, ready}, planned{order: make([]Weighed, 2)})
		h := http.Header{}
		h.Set("x-claude-code-session-id", "keep-rest-seat")
		body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
		_, _, _, key := affine("t", provider.AffinitySession, false, false, false, h, provider.Chat, body, body, cs, pl)
		answered(key, seat, 1, Usage{CacheRead: 50_000})
		t.Cleanup(func() { sticks.Lock(); delete(sticks.m, key); sticks.Unlock() })
		got, _, aff, _ := affine("t", provider.AffinitySession, false, false, false, h, provider.Chat, body, body, cs, pl)
		if keep {
			if aff.Why != "keep" || !aff.Kept {
				t.Fatalf("why %s, kept %v, want keep and kept", aff.Why, aff.Kept)
			}
			if got[0].rest != "kimi@two" {
				t.Fatalf("the resting seat was put in front: first %s", got[0].rest)
			}
			continue
		}
		if aff.Why != "resting" || aff.Kept {
			t.Fatalf("no setting: why %s, kept %v, want resting and unkept", aff.Why, aff.Kept)
		}
	}
}

// keepKeeper is an Anthropic Messages upstream with two keys: k1 answers
// its first ask, refuses its second with the transient 429 the reporter
// logged (Kimi Code's "The engine is currently overloaded", a 429 with a
// short Retry-After), and answers again after. k2 always answers.
type keepKeeper struct {
	mu    sync.Mutex
	tried []string
	asks  map[string]int
}

func (k *keepKeeper) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	key := r.Header.Get("x-api-key")
	if key == "" {
		key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	k.mu.Lock()
	k.tried = append(k.tried, key)
	n := k.asks[key]
	k.asks[key] = n + 1
	k.mu.Unlock()
	if key == "k1" && n == 1 {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"message":"The engine is currently overloaded, please try again later"}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, ev := range []string{
		`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":12,"output_tokens":1,"cache_creation_input_tokens":3000}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"from ` + key + `"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
		`{"type":"message_stop"}`,
	} {
		var t struct {
			Type string `json:"type"`
		}
		json.Unmarshal([]byte(ev), &t)
		io.WriteString(w, "event: "+t.Type+"\ndata: "+ev+"\n\n")
	}
}

func (k *keepKeeper) mark() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.tried)
}

func (k *keepKeeper) since(n int) []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.tried[n:]...)
}

// Two turns of one conversation, then one while the refusal's rest still
// holds, then one after it is over: a keeping pool's turn goes back to
// the key that has the cache, and the ready one that answered meanwhile
// never takes the conversation over. A pool without the setting moves it
// for good, as before.
func TestPoolKeepSendsTheConversationBackAfterTheRest(t *testing.T) {
	const (
		first = `{"model":"kk/m","messages":[{"role":"user","content":"hi"}]}`
		again = `{"model":"kk/m","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"},{"role":"user","content":"more"}]}`
	)
	for _, keep := range []bool{true, false} {
		name := "moves"
		if keep {
			name = "keeps"
		}
		t.Run(name, func(t *testing.T) {
			fresh(t)
			v := &keepKeeper{asks: map[string]int{}}
			up := httptest.NewServer(v)
			t.Cleanup(up.Close)
			p := provider.Provider{ID: "kk", Name: "KK", Key: "k1", Models: []string{"m"}, Anthropic: up.URL + "/v1",
				Keys: []provider.KeyAccount{{Key: "k2"}}, KeepThroughRest: keep}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			s := New()
			if code, body := postAs(t, s, "k1sess", first); code != 200 {
				t.Fatalf("first turn %d %s", code, body)
			}
			if got := v.since(v.mark() - 1); len(got) != 1 || got[0] != "k1" {
				t.Fatalf("first turn tried %v, want k1", got)
			}
			// second turn: the pool's first key is refused with the short
			// 429 and the other key takes the request
			if code, body := postAs(t, s, "k1sess", again); code != 200 {
				t.Fatalf("second turn %d %s", code, body)
			}
			if got := v.since(v.mark() - 2); len(got) != 2 || got[0] != "k1" || got[1] != "k2" {
				t.Fatalf("second turn tried %v, want k1 then k2", got)
			}
			// while the rest holds, the ready key answers and takes the
			// conversation over on its own cache — unless the pool keeps:
			// then the seat stays on the refused one
			if code, body := postAs(t, s, "k1sess", again); code != 200 {
				t.Fatalf("mid-turn %d %s", code, body)
			}
			a := s.trace.routes[len(s.trace.routes)-1].Affinity
			if keep && (a == nil || a.Why != "keep" || !a.Kept) {
				t.Fatalf("mid-turn affinity %+v, want keep and kept", a)
			}
			if !keep && (a == nil || a.Why != "cache" || !a.Kept) {
				t.Fatalf("mid-turn affinity %+v, want the other key's cache keeping", a)
			}
			// the rest runs out; the trace names what rested
			for _, w := range s.trace.routes[len(s.trace.routes)-1].Order {
				if w.Rest != nil {
					expire(w.ID)
				}
			}
			if code, body := postAs(t, s, "k1sess", again); code != 200 {
				t.Fatalf("turn after the rest %d %s", code, body)
			}
			if got := v.since(v.mark() - 1); len(got) != 1 {
				t.Fatalf("turn after the rest tried %v", got)
			} else if keep && got[0] != "k1" {
				t.Fatalf("a keeping pool went back to %s, want k1 (its cache)", got[0])
			} else if !keep && got[0] != "k2" {
				t.Fatalf("without the setting the turn went back to %s, want k2", got[0])
			}
		})
	}
}
