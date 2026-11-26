package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestCodexAccessOnlyStandInWithoutLocalLogin (#6141): a gateway with no local
// Codex sign-in (~/.codex/auth.json absent) must still offer the codex provider
// backed by an imported, enabled access-only ChatGPT account, sign/renew via
// savedLoginToken/renewSavedLoginFrom with zero OAuth refresh calls and without
// creating ~/.codex/auth.json, omit the signed-out exclusion, and avoid
// duplicating the stand-in entry in AlsoOn while still including additional
// enabled saved accounts.
func TestCodexAccessOnlyStandInWithoutLocalLogin(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CODEX_HOME", "")

	var refreshes atomic.Int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshes.Add(1)
		http.Error(w, "unexpected refresh call", http.StatusInternalServerError)
	}))
	defer tokenSrv.Close()
	defer CodexTokenURLForTest(tokenSrv.URL)()

	makeExport := func(email, acctID string, exp time.Time) string {
		tok := fakeJWT(map[string]any{"exp": float64(exp.Unix()), "sub": email})
		idTok := fakeJWT(map[string]any{
			"email": email,
			"https://api.openai.com/auth": map[string]any{
				"chatgpt_plan_type":  "plus",
				"chatgpt_account_id": acctID,
			},
		})
		b, err := json.Marshal(map[string]any{
			"auth_mode": "chatgpt",
			"tokens": map[string]any{
				"id_token":     idTok,
				"access_token": tok,
				"account_id":   acctID,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	later := time.Now().Add(48 * time.Hour)
	// 0. Regular (non-access-only) and disabled access-only saved accounts must NOT
	// become a stand-in: signed-out reporting still applies.
	if err := writeLogins([]savedLogin{
		{Agent: "codex", User: "regular@example.com", On: true, Auth: []byte(makeExport("regular@example.com", "acct-reg", later))},
		{Agent: "codex", User: "off@example.com", On: false, AccessOnly: true, Auth: []byte(makeExport("off@example.com", "acct-off", later))},
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := codexAccount(home); ok {
		t.Fatalf("codexAccount(home) = true without any enabled access-only account, want false")
	}
	sawExcluded := false
	for _, x := range Excluded() {
		if x.Agent == "codex" && x.SignedOut {
			sawExcluded = true
		}
	}
	if !sawExcluded {
		t.Fatalf("expected signed-out codex exclusion when no enabled access-only account exists")
	}
	if err := writeLogins(nil); err != nil {
		t.Fatal(err)
	}

	res, err := ImportCodexAccessOnly(context.Background(), []string{
		makeExport("first@example.com", "acct-first", later),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Status != "added" {
		t.Fatalf("import result = %+v", res)
	}

	// No local ~/.codex/auth.json must exist.
	authPath := filepath.Join(home, ".codex", "auth.json")
	if _, err := os.Stat(authPath); !os.IsNotExist(err) {
		t.Fatalf("expected no local Codex auth.json, stat err = %v", err)
	}

	// 1. Provider discovery must offer codex backed by the access-only stand-in.
	p, ok := codexAccount(home)
	if !ok {
		t.Fatalf("codexAccount(home) = false, want true with imported access-only account")
	}
	if p.Account == nil || p.Account.User != "first@example.com" || !p.Account.StandIn() || p.Account.AgentsOwn() {
		t.Fatalf("unexpected stand-in account: %+v", p.Account)
	}

	// 2. Excluded() must NOT emit a signed-out codex exclusion.
	for _, x := range Excluded() {
		if x.Agent == "codex" && x.SignedOut {
			t.Fatalf("unexpected codex signed-out exclusion: %+v", x)
		}
	}

	// 3. AlsoOn() must NOT duplicate the stand-in entry account.
	if also := p.AlsoOn(); len(also) != 0 {
		t.Fatalf("AlsoOn() with single access-only account = %d entries, want 0", len(also))
	}

	// 4. Signing a request must use the saved access token and account ID without calling refresh.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, CodexBase+"/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Account.sign(context.Background(), req, nil); err != nil {
		t.Fatalf("sign() error: %v", err)
	}
	authHdr := req.Header.Get("Authorization")
	if !strings.HasPrefix(authHdr, "Bearer ") || len(authHdr) <= len("Bearer ") {
		t.Fatalf("Authorization header = %q", authHdr)
	}
	if got := req.Header.Get("ChatGPT-Account-Id"); got != "acct-first" {
		t.Fatalf("ChatGPT-Account-Id = %q, want acct-first", got)
	}
	if refreshes.Load() != 0 {
		t.Fatalf("refresh endpoint called %d times during sign, want 0", refreshes.Load())
	}

	// 5. Importing a second access-only account adds it to AlsoOn without duplicating the stand-in,
	// and pausing/unpausing the stand-in via SetLoginOn works while another account is on.
	if err := SetLoginOn("codex", "first@example.com", false); err == nil {
		t.Fatalf("SetLoginOn(false) on sole stand-in succeeded, want refusal")
	}
	res, err = ImportCodexAccessOnly(context.Background(), []string{
		makeExport("second@example.com", "acct-second", later),
	})
	if err != nil || len(res) != 1 || res[0].Status != "added" {
		t.Fatalf("second import = %+v, %v", res, err)
	}
	also := p.AlsoOn()
	if len(also) != 1 || also[0].Account == nil || also[0].Account.User != "second@example.com" {
		t.Fatalf("AlsoOn() with two access-only accounts = %+v, want [second@example.com]", also)
	}
	if err := SetLoginOn("codex", "first@example.com", false); err != nil {
		t.Fatalf("SetLoginOn(false) with second account on: %v", err)
	}
	if !p.OwnPaused() {
		t.Fatalf("OwnPaused() = false after pausing stand-in, want true")
	}
	if err := SetLoginOn("codex", "first@example.com", true); err != nil {
		t.Fatalf("SetLoginOn(true) unpause stand-in: %v", err)
	}
	if p.OwnPaused() {
		t.Fatalf("OwnPaused() = true after unpausing stand-in, want false")
	}

	// 6. Upstream 401 recovery (renew with stale token) must lapse the access-only
	// account via accessOnlyToken with ZERO OAuth refresh calls and no local auth.json.
	staleTok := strings.TrimPrefix(authHdr, "Bearer ")
	if _, _, err := p.Account.renew(context.Background(), staleTok); !errors.As(err, new(refreshRefused)) {
		t.Fatalf("renew(stale) err = %v, want refreshRefused", err)
	}
	if refreshes.Load() != 0 {
		t.Fatalf("refresh endpoint called %d times during 401 renew, want 0", refreshes.Load())
	}
	if _, err := os.Stat(authPath); !os.IsNotExist(err) {
		t.Fatalf("renew created local Codex auth.json: %v", err)
	}
	// After first@example.com lapses, second@example.com (usable) becomes the stand-in.
	p2, ok := codexAccount(home)
	if !ok || p2.Account == nil || p2.Account.User != "second@example.com" {
		t.Fatalf("after first lapsed, codexAccount = (%v, %+v), want second@example.com", ok, p2.Account)
	}

	// 7. Corrupt/unreadable logins.json with no fallback fails closed (no provider offered).
	_ = os.Remove(loginsPath() + ".bak")
	_ = os.Remove(loginsPath() + ".good")
	if err := os.WriteFile(loginsPath(), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	lastLoginsMu.Lock()
	lastLogins, loginsKnown = nil, false
	lastLoginsMu.Unlock()
	forgetAccountCaches()
	if _, ok := codexAccount(home); ok {
		t.Fatalf("codexAccount(home) succeeded on corrupt logins.json, want false")
	}
}
