package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// cacheWriter is an Anthropic Messages upstream that answers as each key,
// reporting the cache as Anthropic does: a first turn writes the prompt
// (cache_creation_input_tokens) and reads none of it.
type cacheWriter struct {
	mu          sync.Mutex
	tried       []string
	read, wrote int
	// wrote1h is how many of wrote were ephemeral_1h_input_tokens. 0 leaves
	// the usage as a write whose TTL the reply does not split, which is how
	// a 5-minute write is reported when the vendor gives no 1h count.
	wrote1h int
}

func (c *cacheWriter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	key := r.Header.Get("x-api-key")
	if key == "" {
		key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	c.mu.Lock()
	c.tried = append(c.tried, key)
	read, wrote, wrote1h := c.read, c.wrote, c.wrote1h
	c.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	usage := `"cache_read_input_tokens":` + itoa(read) + `,"cache_creation_input_tokens":` + itoa(wrote)
	if wrote1h > 0 {
		five := wrote - wrote1h
		if five < 0 {
			five = 0
		}
		usage += `,"cache_creation":{"ephemeral_5m_input_tokens":` + itoa(five) + `,"ephemeral_1h_input_tokens":` + itoa(wrote1h) + `}`
	}
	for _, ev := range []string{
		`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":12,"output_tokens":1,` + usage + `}}}`,
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

// What the last reply wrote to the vendor's cache counts with what it
// read: the first turn of a conversation reads nothing and writes the
// whole prompt, and its second turn stays to read it (vilicvane on
// Discord: 「会话保持 → 自动」 moved the second turn as "no-cache", and the
// cache just built was thrown away). Kept across a restart too, as the
// file has it; a reply that neither read nor wrote enough still moves.
func TestAffinityKeepsACacheJustWritten(t *testing.T) {
	const (
		first = `{"model":"cw/m","messages":[{"role":"user","content":"hi"}]}`
		again = `{"model":"cw/m","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"},{"role":"user","content":"more"}]}`
	)
	for _, c := range []struct {
		name        string
		read, wrote int
		restart     bool
		why         string
	}{
		{"written", 0, 3000, false, "cache"},
		{"written, restarted", 0, 3000, true, "cache"},
		{"read and written together", 600, 600, false, "cache"},
		{"too little either way", 300, 300, false, "no-cache"},
	} {
		fresh(t)
		v := &cacheWriter{read: c.read, wrote: c.wrote}
		up := httptest.NewServer(v)
		t.Cleanup(up.Close)
		p := provider.Provider{ID: "cw", Name: "CW", Key: "k1", Models: []string{"m"}, Anthropic: up.URL + "/v1",
			Keys: []provider.KeyAccount{{Key: "k2"}}}
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
		s := New()
		if code, body := postAs(t, s, "s1", first); code != 200 {
			t.Fatalf("%s: first turn %d %s", c.name, code, body)
		}
		if c.restart {
			sticks.Lock()
			sticks.m, sticks.from, sticks.mod = map[string]stick{}, "", time.Time{}
			sticks.Unlock()
			s = New()
		}
		if code, body := postAs(t, s, "s1", again); code != 200 {
			t.Fatalf("%s: second turn %d %s", c.name, code, body)
		}
		a := s.trace.routes[len(s.trace.routes)-1].Affinity
		if a == nil || a.Why != c.why || a.Kept != (c.why == "cache") {
			t.Fatalf("%s: affinity %+v, want %s", c.name, a, c.why)
		}
		if a.CacheRead != c.read || a.CacheWrite != c.wrote {
			t.Fatalf("%s: trace tells read %d wrote %d, want %d and %d", c.name, a.CacheRead, a.CacheWrite, c.read, c.wrote)
		}
	}
}

// ageOnlyStick moves the one conversation's last answer back by, as if
// that long had passed. The gap is injected; the test does not sleep.
func ageOnlyStick(t *testing.T, by time.Duration) {
	t.Helper()
	sticks.Lock()
	defer sticks.Unlock()
	if len(sticks.m) != 1 {
		t.Fatalf("age: %d sticks, want 1", len(sticks.m))
	}
	for k, st := range sticks.m {
		st.at = time.Now().Add(-by)
		sticks.m[k] = st
	}
}

// A reply that says the whole write was for an hour (ephemeral_1h_input_tokens)
// is still worth keeping after five minutes, until the hour. Least used
// puts the other key first once the first has been used, so a move shows.
// A 5-minute write, an hour's write whose 1h share is under cacheWorth, and
// an hour's write older than an hour still move. The hour's count is read
// back from affinity.json after a restart.
func TestAffinityKeepsAnHourCachePastFiveMinutes(t *testing.T) {
	const (
		first = `{"model":"cw/m","messages":[{"role":"user","content":"hi"}]}`
		again = `{"model":"cw/m","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"},{"role":"user","content":"more"}]}`
	)
	for _, c := range []struct {
		name           string
		wrote, wrote1h int
		age            time.Duration
		restart        bool
		why, key       string
	}{
		{"1h write, past five minutes", 3000, 3000, cacheCold + time.Minute, false, "cache", "k1"},
		{"1h write, past five minutes, restarted", 3000, 3000, cacheCold + time.Minute, true, "cache", "k1"},
		{"5m write, past five minutes", 3000, 0, cacheCold + time.Minute, false, "cold", "k2"},
		{"1h write, past the hour", 3000, 3000, time.Hour + time.Minute, false, "cold", "k2"},
		{"hour's share still worth it", 3000, 2000, cacheCold + time.Minute, false, "cache", "k1"},
		{"hour's share too small", 3000, 500, cacheCold + time.Minute, false, "cold", "k2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			v := &cacheWriter{wrote: c.wrote, wrote1h: c.wrote1h}
			up := httptest.NewServer(v)
			t.Cleanup(up.Close)
			p := provider.Provider{ID: "cw", Name: "CW", Key: "k1", Models: []string{"m"}, Anthropic: up.URL + "/v1",
				Keys: []provider.KeyAccount{{Key: "k2"}}}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			if err := provider.SetRouting("cw", provider.LeastUsed); err != nil {
				t.Fatal(err)
			}
			s := New()
			if code, body := postAs(t, s, "s1", first); code != 200 {
				t.Fatalf("first turn %d %s", code, body)
			}
			ageOnlyStick(t, c.age)
			if c.restart {
				saveSticks()
				sticks.Lock()
				sticks.m, sticks.from, sticks.mod = map[string]stick{}, "", time.Time{}
				sticks.Unlock()
				s = New()
			}
			if code, body := postAs(t, s, "s1", again); code != 200 {
				t.Fatalf("second turn %d %s", code, body)
			}
			a := s.trace.routes[len(s.trace.routes)-1].Affinity
			if len(v.tried) != 2 || v.tried[0] != "k1" || v.tried[1] != c.key || a == nil || a.Why != c.why || a.Kept != (c.why == "cache") {
				t.Fatalf("went %v, affinity %+v, want %s by %s", v.tried, a, c.key, c.why)
			}
			if a.CacheWrite != c.wrote {
				t.Fatalf("trace wrote %d, want %d", a.CacheWrite, c.wrote)
			}
		})
	}
}
