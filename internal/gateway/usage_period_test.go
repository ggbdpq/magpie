package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An unparseable ?period= is refused, not silently answered with another
// period's data (#5985): a caller asking for a period magpie doesn't know
// (reference.md named week and month, which the HTTP layer never took) is
// told so, and with the shapes that work. No period at all keeps the
// default month it has always answered.
func TestUsagePeriodUnknownIsRefused(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	h := lanGuard(New().Handler())
	do := func(path string) (int, string) {
		r := httptest.NewRequest("GET", path, nil)
		r.RemoteAddr = "127.0.0.1:5000"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	for _, p := range []string{"week", "month", "1d", "24h", "bogus", "2026-11-08..2026-11-09..2026-11-10", "last:0"} {
		if c, b := do("/v1/magpie/usage?period=" + p); c != http.StatusBadRequest || !strings.Contains(b, "period") {
			t.Fatalf("GET /v1/magpie/usage?period=%s: %d %s, want 400 naming the period", p, c, b)
		}
		if c, _ := do("/v1/magpie/usage/requests?period=" + p); c != http.StatusBadRequest {
			t.Fatalf("GET /v1/magpie/usage/requests?period=%s: %d, want 400", p, c)
		}
	}
	if _, b := do("/v1/magpie/usage?period=week"); !strings.Contains(b, "today|7d|30d|all") {
		t.Fatalf("the refusal names the shapes that work: %s", b)
	}
	for _, q := range []string{"", "period=", "period=today", "period=7d", "period=30d", "period=all", "period=last:24h", "period=2026-11-08..2026-11-09"} {
		if c, b := do("/v1/magpie/usage?" + q); c != http.StatusOK {
			t.Fatalf("GET /v1/magpie/usage?%s: %d %s, want 200", q, c, b)
		}
	}
}
