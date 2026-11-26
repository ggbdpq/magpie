package gateway

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

// Within a single request's fallback walk, a key- or account-wide refusal
// (credit/quota/auth/restricted/suspended) must skip remaining candidates on
// the same restKey rather than sending doomed retries on another model of the
// same exhausted key; model-scoped rests still try sibling models; and
// WorkBuddy's Chinese 429 credit-pack refusal must classify as failCredit
// (#7690).
func TestFallbackSkipsExhaustedSameKeyAndClassifiesWorkBuddyZhCredit(t *testing.T) {
	t.Run("skips sibling model on same exhausted key and reaches backup", func(t *testing.T) {
		fresh(t)
		var mu sync.Mutex
		var meooModels []string
		meoo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var q struct {
				Model string `json:"model"`
			}
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &q)
			mu.Lock()
			meooModels = append(meooModels, q.Model)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":{"message":"Meoo: 积分余额不足，请充值后继续使用"}}`)
		}))
		defer meoo.Close()

		backupHits := 0
		backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			backupHits++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"sage","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
		}))
		defer backup.Close()

		if err := provider.Save(provider.Provider{
			ID: "meoo", Name: "Meoo", Key: "k-meoo", Chat: meoo.URL + "/v1",
			Models: []string{"qwen3.8-max", "deepseek-v4-pro-0813"},
		}); err != nil {
			t.Fatal(err)
		}
		if err := provider.Save(provider.Provider{
			ID: "backup", Name: "Backup", Key: "k-backup", Chat: backup.URL + "/v1",
			Models: []string{"sage"},
		}); err != nil {
			t.Fatal(err)
		}
		if err := provider.SaveGroup(provider.Group{
			ID: "mix", Name: "Mix", Routing: provider.Ordered,
			Members: []string{"meoo/qwen3.8-max", "meoo/deepseek-v4-pro-0813", "backup/sage"},
		}); err != nil {
			t.Fatal(err)
		}

		s := New()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions",
			strings.NewReader(`{"model":"group/mix","messages":[{"role":"user","content":"hi"}]}`))
		s.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		mu.Lock()
		gotMeoo := append([]string(nil), meooModels...)
		gotBackup := backupHits
		mu.Unlock()
		if len(gotMeoo) != 1 || gotMeoo[0] != "qwen3.8-max" {
			t.Fatalf("expected meoo called once for qwen3.8-max (skipping sibling on same key), got %v", gotMeoo)
		}
		if gotBackup != 1 {
			t.Fatalf("expected backup called once, got %d", gotBackup)
		}
	})

	t.Run("returns error without retrying sibling when only same key remains", func(t *testing.T) {
		fresh(t)
		var mu sync.Mutex
		var meooModels []string
		meoo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var q struct {
				Model string `json:"model"`
			}
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &q)
			mu.Lock()
			meooModels = append(meooModels, q.Model)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":{"message":"Meoo: 积分余额不足，请充值后继续使用"}}`)
		}))
		defer meoo.Close()

		if err := provider.Save(provider.Provider{
			ID: "meoo", Name: "Meoo", Key: "k-meoo", Chat: meoo.URL + "/v1",
			Models: []string{"qwen3.8-max", "deepseek-v4-pro-0813"},
		}); err != nil {
			t.Fatal(err)
		}
		if err := provider.SaveGroup(provider.Group{
			ID: "onlymeoo", Name: "OnlyMeoo", Routing: provider.Ordered,
			Members: []string{"meoo/qwen3.8-max", "meoo/deepseek-v4-pro-0813"},
		}); err != nil {
			t.Fatal(err)
		}

		s := New()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions",
			strings.NewReader(`{"model":"group/onlymeoo","messages":[{"role":"user","content":"hi"}]}`))
		s.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		mu.Lock()
		gotMeoo := append([]string(nil), meooModels...)
		mu.Unlock()
		if len(gotMeoo) != 1 || gotMeoo[0] != "qwen3.8-max" {
			t.Fatalf("expected meoo called once when only same-key sibling remains, got %v", gotMeoo)
		}
	})

	t.Run("model-scoped refusal still tries sibling model on same key", func(t *testing.T) {
		fresh(t)
		var mu sync.Mutex
		var seen []string
		relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var q struct {
				Model string `json:"model"`
			}
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &q)
			mu.Lock()
			seen = append(seen, q.Model)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if q.Model == "m1" {
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `{"error":{"message":"The model m1 does not exist"}}`)
				return
			}
			_, _ = io.WriteString(w, `{"id":"c2","object":"chat.completion","model":"m2","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
		}))
		defer relay.Close()

		if err := provider.Save(provider.Provider{
			ID: "relay", Name: "Relay", Key: "k-relay", Chat: relay.URL + "/v1",
			Models: []string{"m1", "m2"},
		}); err != nil {
			t.Fatal(err)
		}
		if err := provider.SaveGroup(provider.Group{
			ID: "ms", Name: "ModelScope", Routing: provider.Ordered,
			Members: []string{"relay/m1", "relay/m2"},
		}); err != nil {
			t.Fatal(err)
		}

		s := New()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions",
			strings.NewReader(`{"model":"group/ms","messages":[{"role":"user","content":"hi"}]}`))
		s.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		mu.Lock()
		got := append([]string(nil), seen...)
		mu.Unlock()
		if len(got) != 2 || got[0] != "m1" || got[1] != "m2" {
			t.Fatalf("expected [m1 m2] on model-scoped refusal, got %v", got)
		}
	})

	t.Run("WorkBuddy Chinese 429 credits exhausted is failCredit", func(t *testing.T) {
		wbZh := []byte(`{"error":{"message":"WorkBuddy: 额度已用尽，请访问以下链接，购买加量包以获取更多额度：https://www.codebuddy.cn/profile/usage"}}`)
		if got := failure(http.StatusTooManyRequests, wbZh); got != failCredit {
			t.Fatalf("WorkBuddy Zh 429: got %q, want %q", got, failCredit)
		}
		// Plain time-resetting quota stays failQuota.
		quotaZh := []byte(`{"error":{"message":"您的今日额度已用尽，将于 2026-12-01 00:00:00 UTC+8 重置"}}`)
		if got := failure(http.StatusTooManyRequests, quotaZh); got != failQuota {
			t.Fatalf("resetting quota Zh 429: got %q, want %q", got, failQuota)
		}
	})
}
