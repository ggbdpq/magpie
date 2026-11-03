package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// zaidu259434 on X: a routing group on another computer's magpie ("永不卡住")
// had one member, Open Design, out of its Coding Plan quota and wallet. The
// remote answered 402, its own hold naming when it would ask again
// (Retry-After), and this magpie rested the remote's group model until then
// and gave the 402 again on every request, never asking the remote. The
// user moved the group to another vendor there, and Codex here still got
// Open Design's refusal: the remote magpie routes its own groups and holds
// its own rests, and what it serves under a name can change at any moment,
// so a refusal of its is never given again here without asking it.
func TestRemoteMagpieQuotaIsAskedNotReplayed(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	spent := true
	asked := 0
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		asked++
		out := spent
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if out {
			// what a remote magpie holding its group's lone member says
			w.Header().Set("Retry-After", "3600")
			w.Header().Set(providerHeader, "opendesign")
			w.WriteHeader(http.StatusPaymentRequired)
			io.WriteString(w, `{"error":{"type":"api_error","message":"Open Design: [code=insufficient_balance] Coding Plan quota is exhausted and wallet balance is insufficient (out of its allowance: not asked again before 06:19 UTC+1)"}}`)
			return
		}
		io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"from `+req.Model+` on its new member"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(remote.Close)
	if err := provider.Save(provider.Provider{ID: "office", Name: "Remote magpie", Preset: provider.RemoteMagpiePreset, Key: "k",
		Chat: remote.URL + "/v1", Models: []string{"group/never-stuck"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	body := `{"model":"office/group/never-stuck","messages":[{"role":"user","content":"hi"}]}`
	if code, got := postAs(t, s, "", body); code != http.StatusPaymentRequired {
		t.Fatalf("the remote's 402: %d %s", code, got)
	}
	// a second refusal, as the agent's retry meets it
	postAs(t, s, "", body)
	// the group is changed on the remote
	mu.Lock()
	spent, asked = false, 0
	mu.Unlock()
	code, got := postAs(t, s, "", body)
	mu.Lock()
	n := asked
	mu.Unlock()
	if code != http.StatusOK || !strings.Contains(got, "new member") || n != 1 {
		t.Fatalf("after the group changed on the remote: %d %s; the remote was asked %d times", code, got, n)
	}
}

// XIQI's #4202: a group on another computer's magpie whose lone member was
// refused with a 429 that named a session limit ("You've hit your session
// limit · resets 3pm"), so this magpie rested the remote's model for as
// long as the Retry-After it passed on said (an hour, the most a Retry-After
// is trusted), and gave the 429 again on every request for the rest of the
// hour — the remote's own quota had come back an hour before that, and the
// client was still told the session limit. A 429 out of quota rests that
// model of the remote as a 402 does (remoteModelRest), so it is asked again
// all the same: the remote holds its own rests, and asks the official one
// again as its own rest ends.
func TestRemoteMagpie429IsAskedNotReplayed(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	spent := true
	asked := 0
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		asked++
		out := spent
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if out {
			// what the remote says holding its group's lone member against
			// the official "session limit · resets 3pm": its own Retry-After
			// for when it will ask the official one again
			w.Header().Set("Retry-After", "3600")
			w.Header().Set(providerHeader, "claude")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"type":"rate_limit_error","message":"Magpie: Claude Code: You've hit your session limit · resets 3pm (Asia/Singapore)"}}`)
			return
		}
		// the model is a Claude one, asked on Anthropic's Messages as the
		// remote serves it there
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []string{
			`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"` + req.Model + `","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"once the official reset came"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`{"type":"message_stop"}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", event)
		}
	}))
	t.Cleanup(remote.Close)
	if err := provider.Save(provider.Provider{ID: "office", Name: "Remote magpie", Preset: provider.RemoteMagpiePreset, Key: "k",
		Chat: remote.URL + "/v1", Models: []string{"group/claude-sonnet-5.5"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	body := `{"model":"office/group/claude-sonnet-5.5","messages":[{"role":"user","content":"hi"}]}`
	if code, got := postAs(t, s, "", body); code != http.StatusTooManyRequests {
		t.Fatalf("the remote's 429: %d %s", code, got)
	}
	// a second refusal, as the agent's retry meets it
	postAs(t, s, "", body)
	// the official quota comes back on the remote, as a reset does
	mu.Lock()
	spent, asked = false, 0
	mu.Unlock()
	code, got := postAs(t, s, "", body)
	mu.Lock()
	n := asked
	mu.Unlock()
	if code != http.StatusOK || !strings.Contains(got, "once the official reset came") || n != 1 {
		t.Fatalf("after the official reset came: %d %s; the remote was asked %d times", code, got, n)
	}
}

// The same for a magpie added by hand, as a plain OpenAI-compatible
// provider: its reply names the provider that answered there.
func TestHandAddedMagpieQuotaIsAskedNotReplayed(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	spent := true
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		out := spent
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if out {
			w.Header().Set("Retry-After", "3600")
			w.Header().Set(providerHeader, "opendesign")
			w.WriteHeader(http.StatusPaymentRequired)
			io.WriteString(w, `{"error":{"type":"api_error","message":"Open Design: [code=insufficient_balance] wallet balance is insufficient"}}`)
			return
		}
		io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"answered"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(remote.Close)
	if err := provider.Save(provider.Provider{ID: "desk", Name: "Desk", Key: "k",
		Chat: remote.URL + "/v1", Models: []string{"group/g"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	body := `{"model":"desk/group/g","messages":[{"role":"user","content":"hi"}]}`
	postAs(t, s, "", body)
	postAs(t, s, "", body)
	mu.Lock()
	spent = false
	mu.Unlock()
	if code, got := postAs(t, s, "", body); code != http.StatusOK || !strings.Contains(got, "answered") {
		t.Fatalf("after the group changed there: %d %s", code, got)
	}
}
