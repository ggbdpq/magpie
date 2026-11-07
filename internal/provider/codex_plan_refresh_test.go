package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// planRefreshFakes signs me@ in while on Free, saved to logins.json, and
// work@ in as Codex's own, so me@'s copy renews where it is kept
// (logins_on.go) and not in Codex's auth.json. The token refresh is
// pointed at a fake that answers every refresh with tokens saying Plus:
// the account upgraded behind magpie's back.
func planRefreshFakes(t *testing.T) string {
	t.Helper()
	home := signIn(t)
	// me@ was saved while on Free
	exp := float64(time.Now().Add(time.Hour).Unix())
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      fakeJWT(map[string]any{"email": "me@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "free", "chatgpt_account_id": "acct-1"}}),
			"access_token":  fakeJWT(map[string]any{"exp": exp}),
			"refresh_token": "r", "account_id": "acct-1",
		},
	})
	resetRevoked() // marks are read from this HOME's logins.json
	t.Cleanup(resetRevoked)
	rememberLogins(true)
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)
	plus := fakeJWT(map[string]any{"email": "me@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": "acct-1"}})
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"access_token": plus, "id_token": plus, "refresh_token": "r-2"})
	}))
	t.Cleanup(refresh.Close)
	oldURL := codexTokenURL
	codexTokenURL = refresh.URL
	t.Cleanup(func() { codexTokenURL = oldURL })
	return home
}

// savedPlanOf is the plan a saved account's row carries.
func savedPlanOf(t *testing.T, user string) string {
	t.Helper()
	for _, l := range readLogins() {
		if l.User == user {
			return l.Plan
		}
	}
	t.Fatalf("no saved account %q", user)
	return ""
}

// A saved ChatGPT account's plan can change behind magpie's back: Free
// upgraded to Plus, and the tokens the refresh answers with say so. The
// plan the copy was saved with stayed as it was, and the routing went on
// sorting the account into the free tier with it (OutmanSay, #5593). A
// refresh keeps the saved plan as the fresh tokens name it, the way the
// ChatGPT API's refresh does (siwcFresh).
func TestSavedRefreshUpdatesPlan(t *testing.T) {
	planRefreshFakes(t)
	if got := savedPlanOf(t, "me@example.com"); got != "free" {
		t.Fatalf("saved as %q, want free", got)
	}

	if _, _, err := renewSavedLogin(context.Background(), "codex", "me@example.com", true); err != nil {
		t.Fatal(err)
	}

	// the refreshed sign-in is kept, and the plan follows what its tokens
	// say now
	for _, l := range readLogins() {
		if l.User == "me@example.com" && !strings.Contains(string(l.Auth), `"r-2"`) {
			t.Fatalf("the refreshed sign-in wasn't kept: %s", l.Auth)
		}
	}
	if got := savedPlanOf(t, "me@example.com"); got != "plus" {
		t.Fatalf("after the refresh the saved plan is %q, want plus", got)
	}
}
