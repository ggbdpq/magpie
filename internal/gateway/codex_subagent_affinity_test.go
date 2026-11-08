package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/routes"
)

// codexSignedThree is Codex signed in to ChatGPT with two more of its
// accounts on in magpie (acct-1, acct-2, acct-3), its models cached, the
// ChatGPT backend a fake noting the account of each request, routing over
// the three by rotation, a conversation kept to who answered it for the
// session, and the routing's own state of sticks and rests fresh.
func codexSignedThree(t *testing.T, routing string) (served func() string) {
	t.Helper()
	codexSignedIn(t, "spare2@example.com", "spare3@example.com")
	home, _ := os.UserHomeDir()
	os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"), []byte(`{"etag":"\"v1\"","models":[
		{"slug":"gpt-5.5","display_name":"GPT-5.5","visibility":"list","priority":1,"context_window":272000}]}`), 0o644)
	var mu sync.Mutex
	var got []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			w.WriteHeader(404)
			return
		}
		mu.Lock()
		got = append(got, r.Header.Get("chatgpt-account-id"))
		id := strconv.Itoa(len(got))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r`+id+`","model":"`+modelOf(b)+`"}}`,
			`data: {"type":"response.output_text.delta","delta":"on it"}`,
			`data: {"type":"response.completed","response":{"id":"r`+id+`","usage":{"input_tokens":7,"output_tokens":1}}}`))
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	p.Routing = routing
	p.Affinity = provider.AffinitySession
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	routes.Forget("codex")
	sticks.Lock()
	sticks.m, sticks.from, sticks.mod = map[string]stick{}, "", time.Time{}
	sticks.Unlock()
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	return func() string { mu.Lock(); defer mu.Unlock(); return got[len(got)-1] }
}

// Foreverrainy on #5698: six Codex multi-agent V1 subagents over a
// three-account WorkBuddy group, a round of tasks each, spread two to an
// account; the second round drew them all to one account. Every request of
// the chat names the chat's own session_id, the lead's and its subagents'
// alike, so affinity kept one stick for the parent session and the twelve
// requests of the two rounds shared it: the second round read kept=true,
// why=session, and went wherever the round before had left that one stick.
//
// A subagent's turns are a conversation of their own, on the account whose
// cache holds its thread: each keeps its own stick, by its thread id
// (codexSubagentThread), as a Claude Code subagent does by its agent id,
// and the lead's stick stays on the session's key as it was. The same
// over In turn (the group's own rotation) and over Smart: under Smart the
// first round spreads by what the accounts already hold, a subagent's
// conversation counted there as one of its own ("|thread:" keys are not
// Claude Code's "|agent:", which withLead keeps with the session).
func TestCodexSubagentsKeepTheirOwnAccounts(t *testing.T) {
	for _, routing := range []string{provider.Rotate, ""} {
		t.Run(routingName(routing), func(t *testing.T) {
			testCodexSubagentsKeepTheirOwnAccounts(t, routing)
		})
	}
}

func routingName(routing string) string {
	if routing == "" {
		return "smart"
	}
	return routing
}

func testCodexSubagentsKeepTheirOwnAccounts(t *testing.T, routing string) {
	served := codexSignedThree(t, routing)
	s := New()
	t.Cleanup(s.subscription.abortAll)
	h := s.Handler()

	var mu sync.Mutex
	var bad []string
	ask := func(thread, parent string) (account string, aff *Affinity) {
		t.Helper()
		body := `{"model":"gpt-5.5","stream":true,"input":[` + workerAsk + `]}`
		req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer chatgpt-token")
		req.Header.Set("chatgpt-account-id", "acct-1")
		req.Header.Set("User-Agent", "codex_cli_rs/0.162.1 (Mac OS 26.2.0; arm64)")
		req.Header.Set("session_id", "root-1")
		meta := `{"session_id":"root-1","thread_id":"` + thread + `"`
		if parent != "" {
			req.Header.Set("x-openai-subagent", "collab_spawn")
			req.Header.Set("x-codex-parent-thread-id", parent)
			meta += `,"parent_thread_id":"` + parent + `"`
		}
		req.Header.Set("x-codex-turn-metadata", meta+"}")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			mu.Lock()
			bad = append(bad, strconv.Itoa(rec.Code)+" "+thread)
			mu.Unlock()
			return "", nil
		}
		route := s.trace.routes[len(s.trace.routes)-1]
		return served(), route.Affinity
	}

	// the six spawn and their first round of tasks with it, one after
	// another as they land: a conversation nobody has answered goes where
	// the fewest others in scope are held (rotate), so the three threads
	// start on three accounts
	var first [3]string
	for i, thread := range []string{"worker-1", "worker-2", "worker-3"} {
		first[i], _ = ask(thread, "root-1")
	}
	mu.Lock()
	failed := append([]string(nil), bad...)
	mu.Unlock()
	if len(failed) > 0 {
		t.Fatalf("the first round: %v", failed)
	}
	distinct := map[string]bool{}
	for _, a := range first {
		distinct[a] = true
	}
	if len(distinct) < 2 {
		t.Fatalf("the first round went to %v, not over the accounts", first)
	}

	// the lead's own turn in between, then the second round, one after
	// another as send_input brings each agent back
	lead, _ := ask("root-1", "")
	for i, thread := range []string{"worker-1", "worker-2", "worker-3"} {
		account, aff := ask(thread, "root-1")
		if account != first[i] {
			t.Errorf("%s's second turn went to %s, not back to %s", thread, account, first[i])
		}
		if aff == nil || !aff.Kept || aff.Why != "session" {
			t.Errorf("%s's second turn: kept %+v, want kept for the session", thread, aff)
		}
	}
	if again, _ := ask("root-1", ""); again != lead {
		t.Errorf("the lead's next turn went to %s, not back to %s", again, lead)
	}

	// three sticks of the workers' own, the lead's on the session's key
	// as before (leadFirst reads it there)
	var agent, plain []string
	sticks.Lock()
	for k := range sticks.m {
		if strings.Contains(k, "|thread:") {
			agent = append(agent, k)
		} else {
			plain = append(plain, k)
		}
	}
	sticks.Unlock()
	if len(agent) != 3 {
		t.Errorf("%d sticks of a subagent's own (%v), want one each for the three workers", len(agent), agent)
	}
	if len(plain) != 1 || !strings.HasSuffix(plain[0], "|root-1") {
		t.Errorf("the lead's stick sits on %v, want the session's key alone", plain)
	}
}

// A chat without subagents keeps the affinity it had: the session's key is
// the only one, and its turns stay with the account that answered.
func TestCodexChatWithoutSubagentsKeepsItsAffinity(t *testing.T) {
	served := codexSignedThree(t, provider.Rotate)
	s := New()
	t.Cleanup(s.subscription.abortAll)
	h := s.Handler()
	ask := func() (string, *Affinity) {
		t.Helper()
		body := `{"model":"gpt-5.5","stream":true,"input":[` + workerAsk + `]}`
		req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer chatgpt-token")
		req.Header.Set("chatgpt-account-id", "acct-1")
		req.Header.Set("User-Agent", "codex_cli_rs/0.162.1 (Mac OS 26.2.0; arm64)")
		req.Header.Set("session_id", "solo-1")
		req.Header.Set("x-codex-turn-metadata", `{"session_id":"solo-1","thread_id":"solo-1"}`)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		return served(), s.trace.routes[len(s.trace.routes)-1].Affinity
	}
	one, _ := ask()
	two, aff := ask()
	if two != one {
		t.Fatalf("the second turn went to %s, not back to %s", two, one)
	}
	if aff == nil || !aff.Kept || aff.Why != "session" {
		t.Fatalf("the second turn: kept %+v, want kept for the session", aff)
	}
	var keys []string
	sticks.Lock()
	for k := range sticks.m {
		keys = append(keys, k)
	}
	sticks.Unlock()
	if len(keys) != 1 || !strings.HasSuffix(keys[0], "|solo-1") || strings.Contains(keys[0], "|thread:") || strings.Contains(keys[0], "|agent:") {
		t.Fatalf("the chat's stick sits on %v, want the session's key alone", keys)
	}
}
