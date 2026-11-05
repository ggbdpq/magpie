package gateway

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// burnBoth rests both keys the way a plan's day running out does: the
// quota comes back at, hours from now.
func burnBoth(t *testing.T, hours int, on ...func()) *httptest.ResponseRecorder {
	d, srv := deadChain(t, "dead-a", "dead-b")
	t.Cleanup(func() { _ = d.took() })
	for _, f := range on {
		f()
	}
	at := time.Now().Add(time.Duration(hours)*time.Hour + 21*time.Minute).Truncate(time.Minute)
	if err := provider.SetQuotaResetAt("mockdead", at.UTC().Format("15:04")+"Z"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		postDead(t, srv)
	}
	rec := postDead(t, srv)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("turn-away: %d %s", rec.Code, rec.Body)
	}
	// the confirm asks restLast makes (one resting candidate a request,
	// #1772) are the vendor's business as ever; the cap is about the
	// wait the agent is told, not about burning the accounts
	d.took()
	return rec
}

// #5371 (tlerbao): a quota exhaustion rests the account for hours, and
// the 429 the agent gets carried the whole horizon as Retry-After — an
// agent that runs its own fallback chain (OpenCode clamps a single wait
// at 900s, retries the same model a few times) then stalls ~45 minutes
// before it ever fails over.
func TestRetryAfterGoesOutWholeByDefault(t *testing.T) {
	rec := burnBoth(t, 5)
	ra, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil || ra < 4*3600 {
		t.Fatalf("default: Retry-After %q, want the rest's whole horizon (~5h), err %v", rec.Header().Get("Retry-After"), err)
	}
	if !strings.Contains(rec.Body.String(), "weekly usage limit") {
		t.Errorf("default: the vendor's reason is gone: %s", rec.Body)
	}
}

func TestRetryAfterCapBoundsTheAgentWait(t *testing.T) {
	rec := burnBoth(t, 5, func() {
		// the cap is on before the plan's day runs out: the agent's wait
		// is bounded, the accounts rest the whole horizon just the same
		st := settings.Load()
		sixty := 60
		st.RetryAfterCap = &sixty
		if err := settings.Save(st); err != nil {
			t.Fatal(err)
		}
	})
	ra, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil || ra > 60 {
		t.Fatalf("capped: Retry-After %q, want at most 60s — the agent fails over instead of sleeping the horizon (err %v)", rec.Header().Get("Retry-After"), err)
	}
	if !strings.Contains(rec.Body.String(), "weekly usage limit") {
		t.Errorf("capped: the vendor's reason is gone: %s", rec.Body)
	}
}
