package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #2077: a magpie that uses another computer's (a Remote magpie provider)
// was told by it that Claude Code was out of its session limit, and
// rested its key to that magpie: a request for one of the other magpie's
// Qoder models was refused here with Claude Code's 429, never asked
// there. A refusal from the other magpie for one of its models is that
// model's; only the key's own limit there rests the key.
func TestRemoteMagpieRestsTheModel(t *testing.T) {
	spent := `{"type":"error","error":{"type":"rate_limit_error","message":"You've hit your session limit · resets 8:10pm (America/Los_Angeles)"}}`
	cc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, spent)
	}))
	t.Cleanup(cc.Close)
	var asked atomic.Int32
	q := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		asked.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"fast","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	}))
	t.Cleanup(q.Close)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, p := range []provider.Provider{
		{ID: "cc", Name: "Claude Code", Key: "k1", Models: []string{"claude-opus-5-5"}, Anthropic: cc.URL},
		{ID: "qd", Name: "Qoder", Key: "k2", Models: []string{"fast"}, Chat: q.URL},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	remote := httptest.NewServer(New().Handler())
	t.Cleanup(remote.Close)
	id, err := provider.Add(provider.Provider{ID: "office", Name: "Remote magpie", Preset: provider.RemoteMagpiePreset, Chat: strings.TrimPrefix(remote.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	office, _ := provider.Find(id)
	if _, err := office.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	send := func(model string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`))
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, req)
		return rec
	}
	if rec := send(id + "/cc/claude-opus-5-5"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("Claude Code out of its session limit there: %d %s; want its 429", rec.Code, rec.Body.String())
	}
	rec := send(id + "/qd/fast")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "hello") {
		t.Fatalf("Qoder there, after Claude Code's 429: %d %s; want Qoder's answer", rec.Code, rec.Body.String())
	}
	if asked.Load() != 1 {
		t.Errorf("Qoder was asked %d times; want once", asked.Load())
	}
	// Claude Code itself still rests: asked again, it is refused here
	// without the other magpie being asked
	if rec := send(id + "/cc/claude-opus-5-5"); rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "not asked again") {
		t.Errorf("Claude Code asked again while it rests: %d %s; want its 429 held here", rec.Code, rec.Body.String())
	}
}

// The key's own limit on the other magpie (keyLimited's 429, marked with
// remoteKeyLimitHeader) is the key's whatever model it was asked for: the
// key rests, every model with it.
func TestRemoteMagpieKeyLimitRestsTheKey(t *testing.T) {
	c := candidate{p: provider.Provider{ID: "office", Preset: provider.RemoteMagpiePreset, Key: "k"}, model: "cc/claude-opus-5-5", rest: "office"}
	h := http.Header{}
	if !remoteModelRest(c, failQuota, h) {
		t.Error("a quota refusal from the other magpie rests the key, not the model")
	}
	h.Set(remoteKeyLimitHeader, "2026-10-15T00:00:00Z")
	if remoteModelRest(c, failQuota, h) || remoteModelRest(c, failRate, h) {
		t.Error("the key's own limit there rests only the model, not the key")
	}
	c.p.Preset = ""
	if remoteModelRest(c, failQuota, http.Header{}) {
		t.Error("a provider that isn't a remote magpie rests the model on a quota refusal")
	}
}

// The marker comes by wire: the other magpie's keyLimited answers a 429
// with remoteKeyLimitHeader, and what reaches restAfterMarked here is what
// the relay kept of the upstream reply. The key's own limit there rests
// the whole remote, not the model it was asked for — the opposite of that
// model's own rate limit, which rests it alone
// (TestRemoteMagpieQuotaRestsTheModel). So asked again, the remote's
// models are behind the next member, neither of them asked.
func TestRemoteMagpieKeyLimitRestsTheRemote(t *testing.T) {
	fresh(t)
	forgetSinks(t)
	var mu sync.Mutex
	var asked []string
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		asked = append(asked, req.Model)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if req.Model == "glm/glm-5" {
			// what keyLimited answers with: its 429 names when the key
			// resets, and marks itself the key's own
			w.Header().Set(remoteKeyLimitHeader, "2026-10-19T00:00:00Z")
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"message":"gateway key over its request budget","type":"rate_limit_error"}}`)
			return
		}
		if req.Model == "kimi/kimi-k3" {
			// that model's own rate limit there, no marker: rests alone
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"message":"rate limit exceeded, slow down"}}`)
			return
		}
		io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"from `+req.Model+`"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(remote.Close)
	if err := provider.Save(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Key: "k",
		Chat: remote.URL + "/v1", Models: []string{"glm/glm-5", "kimi/kimi-k3"}}); err != nil {
		t.Fatal(err)
	}
	other := &keyed{}
	serveOn(t, "other", "ko", []string{"m"}, other)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"office/glm/glm-5", "office/kimi/kimi-k3", "other/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	// neither of the remote's models answers: the turn goes to the next
	// member
	if code, body := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`); code != 200 || !strings.Contains(body, "from ko") {
		t.Fatalf("the remote's key limit and its model's rate limit: %d %s", code, body)
	}
	// the key's own limit there rests the remote whole; kimi's own rate
	// limit rests kimi alone
	if _, ok := restOf("office"); !ok {
		t.Errorf("the key's own limit there didn't rest the remote whole: %v", rests())
	}
	if r, ok := restOf("office/glm/glm-5"); ok {
		t.Errorf("the key's own limit rested glm alone: %+v %v", r, rests())
	}
	if _, ok := restOf("office/kimi/kimi-k3"); !ok {
		t.Errorf("kimi's own rate limit there didn't rest kimi alone: %v", rests())
	}
	// asked again, the remote sits out whole: neither model is
	mu.Lock()
	asked = nil
	mu.Unlock()
	if code, body := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`); code != 200 || !strings.Contains(body, "from ko") || len(asked) != 0 {
		t.Fatalf("the remote's models were asked %v after its key limit: %d %s", asked, code, body)
	}
}
