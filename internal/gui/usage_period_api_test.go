package gui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The GUI's usage routes refuse an unparseable ?period= the same way the
// gateway's do (#5985): a value no period parses to is a 400 that names
// the shapes that work, not another period's data. No period keeps the
// default month.
func TestUsagePeriodUnknownRefusedAPI(t *testing.T) {
	sandboxHome(t)
	mux := http.NewServeMux()
	usageRoutes(mux, folderOnly{})
	get := func(path string) (int, string) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w.Code, w.Body.String()
	}
	for _, p := range []string{"week", "month", "bogus"} {
		if c, b := get("/api/usage?period=" + p); c != http.StatusBadRequest {
			t.Fatalf("GET /api/usage?period=%s: %d %s, want 400", p, c, b)
		}
		if c, b := get("/api/usage/requests?period=" + p); c != http.StatusBadRequest {
			t.Fatalf("GET /api/usage/requests?period=%s: %d %s, want 400", p, c, b)
		}
	}
	for _, q := range []string{"", "period=today", "period=7d", "period=30d", "period=all", "period=last:24h", "period=2026-11-08..2026-11-09"} {
		if c, b := get("/api/usage?" + q); c != http.StatusOK {
			t.Fatalf("GET /api/usage?%s: %d %s, want 200", q, c, b)
		}
	}
}
