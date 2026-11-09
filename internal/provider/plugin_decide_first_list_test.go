package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/testenv"
)

// A plugin whose config hook says decide and that lists every model
// itself, none marked a decision model (HMind's, #5793), builds a
// provider that reads decision-only until its first list arrives: decide
// said, no conversation endpoint a listing without models could derive.
// Its fetch then went to the decision API alone, which is empty before
// the first list, and failed with "X's plugin lists no decision models"
// — the account's list, the one that fills the listing in, was never
// asked. Test asked it too, at its early testDecide.

// writePluginFile is a file of magpie's settings folder, as the plugin
// host and magpie keep them.
func writePluginFile(t *testing.T, name string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	os.MkdirAll(settings.Dir(), 0o700)
	if err := os.WriteFile(filepath.Join(settings.Dir(), name), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// hmindBeforeItsList is the provider as magpie serves it before the
// plugin's first model list: decide said, no model listed.
func hmindBeforeItsList(t *testing.T) Provider {
	t.Helper()
	spec := "@example/hmind-auth"
	writePluginFile(t, "plugins.json", map[string]any{"plugins": []map[string]any{{"spec": spec}}})
	writePluginFile(t, "plugin-auth.json", map[string]map[string]any{"hmind": {"type": "api", "key": "sk-hmind"}})
	plugin.UseCached([]plugin.Provider{{
		ID:       "hmind",
		Spec:     spec,
		Name:     "HMind",
		Methods:  []plugin.Method{{Type: "api", Label: "API key"}},
		Decide:   true,
		Accounts: []plugin.Account{{Key: "hmind", Type: "api"}},
	}})
	t.Cleanup(func() { plugin.UseCached(nil) })
	saveLogins(t, savedLogin{Agent: "plugin:hmind", User: "one@hmind", Home: "hmind", On: true})
	ps := pluginAccounts()
	i := slices.IndexFunc(ps, func(p Provider) bool { return p.ID == "hmind" })
	if i < 0 {
		t.Fatalf("no hmind provider among the accounts: %v", ps)
	}
	return ps[i]
}

// Fetching the list of a plugin that reads decision-only before its first
// list asks the plugin's account list, whose answer fills the listing in
// — it doesn't stop at the empty decision API (#5793).
func TestPluginDecideFetchAsksTheListFirst(t *testing.T) {
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	p := hmindBeforeItsList(t)
	if !p.DecideOnly() {
		t.Fatalf("before its first list the provider reads decision-only: chat %q, responses %q, decide %q", p.Chat, p.Responses, p.Decide)
	}
	asked := false
	p.Account.fetch = func(context.Context) ([]catalog.Model, error) {
		asked = true
		return []catalog.Model{{ID: "hmind-chat-1"}, {ID: "hmind-chat-2"}}, nil
	}
	ms, err := p.Fetch(ctx)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !asked {
		t.Fatal("the plugin's account list was never asked")
	}
	if len(ms) != 2 || ms[0].ID != "hmind-chat-1" || ms[1].ID != "hmind-chat-2" {
		t.Fatalf("the fetch gave %v", ms)
	}
}

// Testing the same provider enters no testDecide while its listing has no
// decision model: the decide row it answered is the error #5793 is about.
func TestPluginDecideTestLeavesNoDecideRowWithoutModels(t *testing.T) {
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	p := hmindBeforeItsList(t)
	p.Account.fetch = func(context.Context) ([]catalog.Model, error) {
		return nil, context.DeadlineExceeded
	}
	for _, r := range p.Test(ctx) {
		if r.Protocol == "decide" || strings.Contains(r.Error, "lists no decision models") {
			t.Fatalf("the decide row came back: %+v", r)
		}
	}
}

// The whole way, through the plugin's host: a plugin that says decide and
// lists no model of its own ($FAKE_DECIDE=dynamic) whose vendor refuses
// its list at first. Fetching its models asks the vendor (the account's
// list), not the empty decision API; once the vendor answers, the same
// fetch gives its chat models, and a test probes the conversation
// endpoint it lists instead of saying "FakeCo's plugin lists no decision
// models" (#5793).
func TestPluginDecideFetchBeforeItsFirstList(t *testing.T) {
	bun := testenv.Bun(t)
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Setenv("FAKE_DECIDE", "dynamic")
	t.Setenv("FAKE_MODELS_THROW", "1")
	t.Cleanup(plugin.Settle)
	// the vendor refuses its /models at first (the connection hangs up
	// mid-answer, as a stuck vendor's does), and serves conversations once
	// its list is asked for
	var mu sync.Mutex
	refuse, asked := true, 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/models") {
			asked++
			if refuse {
				if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
					conn.Close()
					return
				}
				panic("the vendor refuses its list")
			}
			w.Write([]byte("later"))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte("data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"from the plugin\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(up.Close)
	t.Setenv("FAKE_BASE", up.URL+"/v1")
	t.Setenv("FAKE_MODELS", up.URL+"/models")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	key, err := plugin.Import(ctx, "fakeco", map[string]any{"type": "oauth", "refresh": "r-ok", "access": "a", "expires": 9e15, "accountId": "a@fake"})
	if err != nil {
		t.Fatal(err)
	}
	saveLogins(t, savedLogin{Agent: "plugin:fakeco", User: "a@fake", Home: key, On: true})
	// the first listing: the models hook asks the vendor, which refuses,
	// and the row keeps what the config said — decide, and no model
	ps, err := plugin.Providers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(ps, func(pp plugin.Provider) bool { return pp.ID == "fakeco" })
	if i < 0 {
		t.Fatalf("no fakeco among the listed: %v", ps)
	}
	if !ps[i].Decide || len(ps[i].Models) != 0 {
		var ids []string
		for _, m := range ps[i].Models {
			ids = append(ids, m.ID)
		}
		t.Fatalf("before its first list the plugin says decide %v with %d models: %v", ps[i].Decide, len(ps[i].Models), ids)
	}
	p, err := Find(PluginID("fakeco"))
	if err != nil {
		t.Fatal(err)
	}
	if !p.DecideOnly() {
		t.Fatalf("before its first list the provider reads decision-only: chat %q, responses %q, anthropic %q, decide %q", p.Chat, p.Responses, p.Anthropic, p.Decide)
	}

	// Fetch models before the first list: the vendor is asked — the
	// account's list, which says why it couldn't — and the answer is not
	// the decision API's empty hand (#5793)
	_, err = p.Fetch(ctx)
	mu.Lock()
	hits, stillRefusing := asked, refuse
	mu.Unlock()
	if hits == 0 {
		t.Fatal("the vendor was never asked for its list")
	}
	if err != nil && strings.Contains(err.Error(), "lists no decision models") {
		t.Fatalf("the fetch stopped at the decision API: %v", err)
	}
	if !stillRefusing {
		t.Fatal("the vendor should still be refusing here")
	}

	// the vendor serves its list now: the same fetch gives its chat models
	mu.Lock()
	refuse = false
	mu.Unlock()
	ms, err := p.Fetch(ctx)
	if err != nil {
		t.Fatalf("fetch after the list: %v", err)
	}
	if !slices.ContainsFunc(ms, func(m catalog.Model) bool { return m.ID == "fake-1" }) {
		t.Fatalf("the fetch after the list gave %v", ms)
	}

	// and a test probes the conversation endpoints the list named, with no
	// decide row saying the plugin lists no decision models
	rs := p.Test(ctx)
	chat := false
	for _, r := range rs {
		if r.Protocol == "decide" || strings.Contains(r.Error, "lists no decision models") {
			t.Errorf("the decide row came back: %+v", r)
		}
		if r.Protocol == "chat" {
			chat = true
		}
	}
	if !chat {
		t.Errorf("no conversation endpoint was tested: %+v", rs)
	}
}
