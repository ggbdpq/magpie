package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// TestRemoteStrictQuotaDoesNotHoldOtherSessions keeps a remote magpie's
// strict session, refused for the account it is bound to there, from
// holding another session of the same model that is bound to an account
// still served there (#4317).
func TestRemoteStrictQuotaDoesNotHoldOtherSessions(t *testing.T) {
	for _, preset := range []string{provider.RemoteMagpiePreset, ""} {
		t.Run("preset="+preset, func(t *testing.T) { remoteStrictQuotaDoesNotHoldOtherSessions(t, preset) })
	}
}

func remoteStrictQuotaDoesNotHoldOtherSessions(t *testing.T, preset string) {
	fresh(t)
	v := &keyed{}
	spent := false
	serveOn(t, "accounts", "ka", []string{"m"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if spent && r.Header.Get("Authorization") == "Bearer ka" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "600")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"message":"quota exhausted"}}`)
			return
		}
		v.ServeHTTP(w, r)
	}), "kb")
	if err := provider.SetRouting("accounts", provider.Rotate); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetAffinity("accounts", provider.AffinityStrict); err != nil {
		t.Fatal(err)
	}
	remote := httptest.NewServer(New().Handler())
	t.Cleanup(remote.Close)
	if err := provider.Save(provider.Provider{ID: "office", Name: "Remote magpie", Preset: preset, Key: "rk",
		Chat: remote.URL + "/v1", Models: []string{"accounts/m"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	body := func(session string) string {
		return `{"model":"office/accounts/m","messages":[{"role":"user","content":"hi ` + session + `"}]}`
	}
	for _, tc := range []struct{ session, key string }{{"one", "ka"}, {"two", "kb"}} {
		if code, got := postAs(t, s, tc.session, body(tc.session)); code != 200 || !strings.Contains(got, "from "+tc.key) {
			t.Fatalf("session %s opening: %d %s; want %s", tc.session, code, got, tc.key)
		}
	}
	spent = true
	if code, got := postAs(t, s, "one", body("one")); code != 429 {
		t.Fatalf("strict session on exhausted ka: %d %s; want 429", code, got)
	}
	if code, got := postAs(t, s, "two", body("two")); code != 200 || !strings.Contains(got, "from kb") {
		t.Fatalf("strict session on available kb blocked by ka's quota: %d %s", code, got)
	}
}
