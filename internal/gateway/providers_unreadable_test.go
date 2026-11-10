package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/provider"
)

// unreadableHome: Codex signed in to ChatGPT (tierAccounts, the account's
// own quota), and a relay first in the Providers order that serves
// gpt-6-luna too, whose replies say "relay". It returns the requests the
// subscription was tried with and the relay's hits.
func unreadableHome(t *testing.T) (codexTried func() []string, relayHits func() int64) {
	t.Helper()
	tried := tierAccounts(t, nil, nil)
	var hits atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		hits.Add(1)
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r3","model":"gpt-6-luna"}}`,
			`data: {"type":"response.output_text.delta","delta":"relay"}`,
			`data: {"type":"response.completed","response":{"id":"r3","usage":{"input_tokens":7,"output_tokens":1}}}`))
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Responses: up.URL + "/v1", Models: []string{"gpt-6-luna"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetOrder([]string{"relay"}); err != nil {
		t.Fatal(err)
	}
	return tried, func() int64 { return hits.Swap(0) }
}

// breakProvidersFile puts a folder where providers.json is: the file is
// there, and reads of it fail, as they do while another program holds it —
// a moment at a start-up on Windows, and the state a broken file stays in.
// The restore it returns puts the file back.
func breakProvidersFile(t *testing.T) (restore func()) {
	t.Helper()
	p := provider.Path()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
	filememo.Look(p)
	restore = func() {
		os.RemoveAll(p)
		os.WriteFile(p, b, 0o600)
		filememo.Look(p)
	}
	t.Cleanup(restore)
	return restore
}

// #5935 (Leelili-BY: after Windows started magpie at boot, requests went
// to the signed-in account's quota instead of the provider's, now and
// then): while providers.json can't be read, the user's providers are out
// of every read of it and the built-in subscriptions stay. A model the
// user's provider serves — asked as relay/model, or by its bare name —
// is then resolved and routed from the built-ins alone: to the agent's
// own subscription, whose quota is spent in the provider's place, without
// a word. magpie refuses instead, and says the file can't be read, so the
// agent asks again and is served once it reads again.
func TestUnreadableProvidersFileRefusesRatherThanTheOwnSubscription(t *testing.T) {
	codexTried, relayHits := unreadableHome(t)
	s := New()
	// readable: the relay answers; the subscription is not asked
	if rec := postCodex(s, "relay/gpt-6-luna"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "relay") {
		t.Fatalf("readable: %d %s", rec.Code, rec.Body)
	}
	if tried := codexTried(); len(tried) != 0 {
		t.Fatalf("readable: the subscription was asked %v", tried)
	}
	if n := relayHits(); n != 1 {
		t.Fatalf("readable: the relay was asked %d times", n)
	}
	restore := breakProvidersFile(t)
	for _, model := range []string{"relay/gpt-6-luna", "gpt-6-luna"} {
		rec := postCodex(s, model)
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "providers.json") {
			t.Fatalf("unreadable, asked for %q: %d %s", model, rec.Code, rec.Body)
		}
	}
	if tried := codexTried(); len(tried) != 0 {
		t.Fatalf("unreadable: the subscription was asked %v", tried)
	}
	if n := relayHits(); n != 0 {
		t.Fatalf("unreadable: the relay was asked %d times", n)
	}
	// readable again: the relay answers, as it did
	restore()
	if rec := postCodex(s, "relay/gpt-6-luna"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "relay") {
		t.Fatalf("readable again: %d %s", rec.Code, rec.Body)
	}
	if tried := codexTried(); len(tried) != 0 {
		t.Fatalf("readable again: the subscription was asked %v", tried)
	}
}

// The bare name goes to the subscription by design while the file reads
// (ownFirst): the unreadable file takes that pick away with the rest.
func TestUnreadableProvidersFileTakesTheBareNamePickAway(t *testing.T) {
	unreadableHome(t)
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	breakProvidersFile(t)
	if to, ok := ownFirst(r, "codex", "gpt-6-luna"); ok {
		t.Fatalf("unreadable: the bare name still picked %q", to)
	}
	if _, _, ok := provider.OwnRoute("codex", "gpt-6-luna"); ok {
		t.Fatal("unreadable: OwnRoute still routes to the subscription")
	}
	if _, _, ok := provider.Resolve("relay/gpt-6-luna"); ok {
		t.Fatal("unreadable: Resolve still answers a provider for it")
	}
	if _, ok := provider.GroupFor("gpt-6-luna"); ok {
		t.Fatal("unreadable: GroupFor still names a group for the bare id")
	}
	if err := provider.FileError(); err == nil {
		t.Fatal("unreadable: FileError says nil")
	}
}
