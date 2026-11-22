package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// wbExhausted is WorkBuddy's 429 for credits used up, as the WorkBuddy
// plugin's tests quote it (plugins #25), and as the plugin passes it on
// (explained): insufficient_quota, the hint after the message.
const (
	wbExhausted          = `{"error":{"data":{"code":14018,"msg":"Credits exhausted. Please visit the link below to purchase add-on packs and get more credits: https://www.codebuddy.ai/profile/usage ","requestId":"test-req"}}}`
	wbExhaustedExplained = `{"error":{"message":"Credits exhausted. Please visit the link below to purchase add-on packs and get more credits: https://www.codebuddy.ai/profile/usage  — WorkBuddy requires an active credit package even for 0-rate free models; check your account package balance","type":"insufficient_quota","code":14018}}`
)

// WorkBuddy's "Credits exhausted" is out of credit, as the built-in gets it
// and as the plugin passes it on: read as a rate limit, the built-in's
// account was benched a minute, then longer each time.
func TestCreditsExhaustedIsOutOfCredit(t *testing.T) {
	for _, body := range []string{wbExhausted, wbExhaustedExplained} {
		if got := failure(429, []byte(body)); got != failCredit {
			t.Errorf("%s: %s, want %s", body, got, failCredit)
		}
	}
	// a rate limit that names credits in passing is still one
	if got := failure(429, []byte(`{"error":{"message":"Rate limit reached for requests; credits are not affected"}}`)); got != failRate {
		t.Errorf("rate limit naming credits: %s", got)
	}
}

// An account out of credits on a model that costs them still serves the
// models its subscription gives free (Creat.wu on Discord: WorkBuddy AI's
// glm-5.3-flash used the credits up, and the account was skipped for its
// free models). Through a plugin, as a moved WorkBuddy's requests go:
// fake-1 is free (x0.00), deepseek-v4-pro costs credits. The refusal
// rests deepseek-v4-pro alone, and the account that refused it is still
// the one fake-1 goes to.
func TestOutOfCreditLeavesFreeModels(t *testing.T) {
	bun := testenv.Bun(t)
	fresh(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Setenv("FAKE_DEEPSEEK", "1")
	t.Cleanup(plugin.Settle)
	old := allowances
	allowances = func(string) map[string]provider.Allowance { return nil }
	t.Cleanup(func() { allowances = old })

	var mu sync.Mutex
	var keys []string // the key each request for fake-1 came with
	up := testenv.FakeVendor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Model string `json:"model"`
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &q)
		if q.Model == "deepseek-v4-pro" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(429)
			io.WriteString(w, wbExhaustedExplained)
			return
		}
		mu.Lock()
		keys = append(keys, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"c","object":"chat.completion.chunk","model":"fake-1","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}`+"\n\n")
		io.WriteString(w, `data: {"id":"c","object":"chat.completion.chunk","model":"fake-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer up.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	// two, as the last one left is told the error without resting
	for _, k := range []string{"k1", "k2"} {
		if _, err := plugin.APIKey(ctx, "fakeco", 0, nil, k, plugin.NewAccount); err != nil {
			t.Fatal(err)
		}
	}
	p, err := provider.Find("fakeco")
	if err != nil {
		t.Fatal(err)
	}
	if !p.PaidBesideFree("deepseek-v4-pro") || p.PaidBesideFree("fake-1") {
		t.Fatalf("deepseek-v4-pro paid beside free %v, fake-1 %v", p.PaidBesideFree("deepseek-v4-pro"), p.PaidBesideFree("fake-1"))
	}
	restingUntil.Lock()
	restingUntil.m, restingUntil.note = map[string]time.Time{}, map[string]Rest{}
	restingUntil.Unlock()

	s := New()
	ask := func(model string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
			strings.NewReader(`{"model":"fakeco/`+model+`","messages":[{"role":"user","content":"hello"}]}`)))
		return rec
	}
	if rec := ask("deepseek-v4-pro"); rec.Code != 429 {
		t.Fatalf("deepseek-v4-pro answered %d %s", rec.Code, rec.Body.String())
	}
	restingUntil.Lock()
	var got []string
	scoped := 0
	for id, r := range restingUntil.note {
		got = append(got, fmt.Sprintf("%s: %v by %s (%s)", id, time.Until(restingUntil.m[id]).Round(time.Minute), r.By, r.Why))
		if r.Why == failCredit && strings.HasSuffix(id, "/deepseek-v4-pro") {
			scoped++
		} else {
			defer t.Errorf("rests more than the model that costs credits: %s by %s (%s)", id, r.By, r.Why)
		}
	}
	restingUntil.Unlock()
	if scoped == 0 {
		t.Fatalf("no rest of deepseek-v4-pro for its credits: %v", got)
	}

	// the free model goes to the first account, as before its credits ran out
	if rec := ask("fake-1"); rec.Code != 200 {
		t.Fatalf("fake-1 answered %d %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(keys) != 1 || keys[0] != "Bearer k1" {
		t.Fatalf("fake-1 went with %v, want the first account's (k1); rests %v", keys, got)
	}
}

// opencodeModels writes models.dev's shape for OpenCode Go (catalog
// "opencode"): the -free models carry no free word anywhere — models.dev's
// API has no free field — only a cost of $0 on every count, beside the paid
// ones (tkhs101's #7496).
func opencodeModels(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte(`{"opencode":{"id":"opencode","name":"OpenCode","models":{
		"glm-5.3":{"id":"glm-5.3","cost":{"input":3,"output":15,"cache_read":0.3,"cache_write":3.75}},
		"hy4-flash-free":{"id":"hy4-flash-free","cost":{"input":0,"output":0,"cache_read":0,"cache_write":0}}
	}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

// ocgoWeekly is OpenCode Go's weekly-limit refusal, as the reporter's
// routing trace has it (tkhs101's #7496).
const ocgoWeekly = `{"error":{"message":"OpenCode Go usage limit reached (weekly, resets in 14h 54m)"}}`

// A key out of its weekly window still serves its cost-zero free models
// (opencode.ai's docs: "If you reach the usage limit, you can continue
// using the free models"): the rest is the paid model's own, and a free
// one is asked as before — not answered the key's kept 429 without going
// out (#7496: 9-10ms of the same 429, the request never sent).
func TestOutOfQuotaLeavesCostZeroFreeModels(t *testing.T) {
	fresh(t)
	opencodeModels(t)
	var mu sync.Mutex
	var asked []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Model string `json:"model"`
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &q)
		mu.Lock()
		asked = append(asked, q.Model)
		mu.Unlock()
		if q.Model == "glm-5.3" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(429)
			io.WriteString(w, ocgoWeekly)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"c","object":"chat.completion.chunk","model":"hy4-flash-free","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}`+"\n\n")
		io.WriteString(w, `data: {"id":"c","object":"chat.completion.chunk","model":"hy4-flash-free","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer up.Close()
	p := provider.Provider{ID: "ocgo", Name: "OpenCode Go", Key: "sk-ocgo", Catalog: "opencode", Chat: up.URL + "/v1"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := provider.Find("ocgo")
	if err != nil {
		t.Fatal(err)
	}
	if got.FreeModel("glm-5.3") || !got.FreeModel("hy4-flash-free") {
		t.Fatalf("glm-5.3 free %v, hy4-flash-free free %v", got.FreeModel("glm-5.3"), got.FreeModel("hy4-flash-free"))
	}

	s := New()
	ask := func(model string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
			strings.NewReader(`{"model":"ocgo/`+model+`","messages":[{"role":"user","content":"hello"}]}`)))
		return rec
	}
	if rec := ask("glm-5.3"); rec.Code != 429 {
		t.Fatalf("glm-5.3 answered %d %s", rec.Code, rec.Body.String())
	}
	restingUntil.Lock()
	var rests []string
	scoped := 0
	for id, r := range restingUntil.note {
		rests = append(rests, fmt.Sprintf("%s: %v by %s (%s)", id, time.Until(restingUntil.m[id]).Round(time.Minute), r.By, r.Why))
		if r.Why == failQuota && id == "ocgo/glm-5.3" {
			scoped++
		} else {
			t.Errorf("rests more than the paid model: %s by %s (%s)", id, r.By, r.Why)
		}
	}
	restingUntil.Unlock()
	if scoped == 0 {
		t.Errorf("no rest of glm-5.3 for the key's window: %v", rests)
	}

	// the free model goes out as before: the vendor answers it, not the
	// key's kept 429
	if rec := ask("hy4-flash-free"); rec.Code != 200 {
		t.Errorf("hy4-flash-free answered %d %s (rests %v)", rec.Code, rec.Body.String(), rests)
	}
	mu.Lock()
	if len(asked) != 2 || asked[0] != "glm-5.3" || asked[1] != "hy4-flash-free" {
		t.Errorf("the vendor saw %v, want glm-5.3 then hy4-flash-free", asked)
	}
	mu.Unlock()
	if !got.PaidBesideFree("glm-5.3") {
		t.Errorf("glm-5.3 is paid beside the cost-zero hy4-flash-free: PaidBesideFree says no")
	}
}

// OpenAI answers an organization whose prepaid API credits are spent with
// 429 and the code credit_balance_exhausted (hey_boyce on X, 11-06). The
// body here carries OpenAI's documented code alone, not a captured reply:
// it is out of credit, so the next account takes the request, not a rate
// limit that asks the same key again in a minute.
func TestOpenAICreditBalanceExhaustedIsCredit(t *testing.T) {
	for _, body := range []string{
		`{"error":{"code":"credit_balance_exhausted"}}`,
		`{"error":{"code":"credit_balance_exhausted","type":"rate_limit_exceeded"}}`,
	} {
		if got := failure(429, []byte(body)); got != failCredit {
			t.Errorf("%s: %s, want %s", body, got, failCredit)
		}
		if minuteRate([]byte(body)) {
			t.Errorf("%s: read as a minute's rate limit", body)
		}
	}
}
