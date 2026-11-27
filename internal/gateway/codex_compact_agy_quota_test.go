package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// agyCompactSetup saves one provider speaking the Responses wire, backed
// by the fake, and nothing beside it.
func agyCompactSetup(t *testing.T, id string, f *fake) {
	t.Helper()
	noRests()
	t.Cleanup(noRests)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: id, Name: id, Key: "k", Models: []string{"gemini-4-hydrogen"}}
	p.Responses = up.URL + "/v1"
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

// agyQuotaRefusal is Antigravity's answer to a summary request, as
// #7996 reproduces it: a 429 the agy-cli plugin relays with the pool's
// quota notice, reset time in the words.
const agyQuotaRefusal = `{"error":{"type":"rate_limit_error","message":"agy bridge: Antigravity quota reached: ` + agyQuotaNotice + `"}}`

// #7996: Antigravity's pool quota used up answers a compaction 429, and
// magpie said it as a rate_limit_error and counted it toward the
// conversation's compaction-failure guard, so Codex read a retryable
// error and asked again while the model was held to its reset, until the
// guard held the conversation besides. Said as Codex's own
// usage_limit_reached, with the reset it names, Codex waits instead of
// retrying; the hold answers the tries after the first without asking
// the vendor again, and none of them counts.
func TestCodexCompactAgyQuotaIsTerminal(t *testing.T) {
	f := &fake{t: t, refuse: func([]byte) (int, string) {
		return http.StatusTooManyRequests, agyQuotaRefusal
	}}
	agyCompactSetup(t, "agy-cli", f)
	for i := range compactFailsMost + 2 {
		code, body := codexPost(t, codexCompactOf("agy-cli/gemini-4-hydrogen", "agy-thread-1"))
		if code != http.StatusTooManyRequests {
			t.Fatalf("try %d: %d %s", i, code, body)
		}
		var q struct {
			Error struct {
				Type     string `json:"type"`
				Message  string `json:"message"`
				ResetsAt int64  `json:"resets_at"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(body), &q); err != nil {
			t.Fatalf("try %d: %v: %s", i, err, body)
		}
		if q.Error.Type != "usage_limit_reached" {
			t.Fatalf("try %d: error type %q, want usage_limit_reached: %s", i, q.Error.Type, body)
		}
		if !strings.Contains(q.Error.Message, "Antigravity quota reached") {
			t.Errorf("try %d: message %q", i, q.Error.Message)
		}
		if q.Error.ResetsAt <= time.Now().Unix() {
			t.Errorf("try %d: resets_at %d, want a reset time ahead", i, q.Error.ResetsAt)
		}
	}
	if f.calls != 1 {
		t.Errorf("asked %d times, want the vendor once, the hold answering the rest", f.calls)
	}
}

// A transient limit on the same model, which says no quota and no reset,
// stays the retryable error it was, counted as it was.
func TestCodexCompactAgyTransientLimitStaysRetryable(t *testing.T) {
	f := &fake{t: t, refuse: func([]byte) (int, string) {
		return http.StatusTooManyRequests, `{"error":{"type":"rate_limit_error","message":"temporarily rate limited, try again later"}}`
	}}
	agyCompactSetup(t, "agy-cli", f)
	code, body := codexPost(t, codexCompactOf("agy-cli/gemini-4-hydrogen", "agy-thread-2"))
	if code != http.StatusTooManyRequests {
		t.Fatalf("%d %s", code, body)
	}
	if !strings.Contains(body, "rate_limit_error") {
		t.Errorf("a transient limit retyped: %s", body)
	}
}

// The same words from a provider that is not the agy plugin stay as they
// were: another vendor's "individual quota reached" says nothing of a
// hold to a reset Codex should wait out (agy_quota_test.go).
func TestCodexCompactAgyWordsFromAnotherProviderStayRetryable(t *testing.T) {
	f := &fake{t: t, refuse: func([]byte) (int, string) {
		return http.StatusTooManyRequests, `{"error":{"type":"rate_limit_error","message":"another bridge: Antigravity quota reached: ` + agyQuotaNotice + `"}}`
	}}
	agyCompactSetup(t, "other", f)
	code, body := codexPost(t, codexCompactOf("other/gemini-4-hydrogen", "agy-thread-3"))
	if code != http.StatusTooManyRequests {
		t.Fatalf("%d %s", code, body)
	}
	if !strings.Contains(body, "rate_limit_error") {
		t.Errorf("another provider's words retyped: %s", body)
	}
}
