package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOwnMCPRejectsOversizedBody(t *testing.T) {
	for _, tc := range []struct {
		name  string
		path  string
		limit int
	}{
		{"vision", VisionMCPPath, visionMCPBody},
		{"search", SearchMCPPath, 1 << 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, upstream := eyed(t)
			h := lanGuard(s.Handler())
			call := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
			if tc.path == VisionMCPPath {
				call = analyzeCall("https://image.invalid/screenshot.png", "Read the screenshot")
			}
			bounded := call + strings.Repeat(" ", tc.limit-len(call))
			post := func(body string) *httptest.ResponseRecorder {
				t.Helper()
				r := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(body))
				r.RemoteAddr, r.Host = "127.0.0.1:50000", "127.0.0.1:3425"
				r.ContentLength = -1
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w
			}
			if w := post(bounded); w.Code != http.StatusOK {
				t.Fatalf("a body exactly at the limit must work: %d %s", w.Code, w.Body)
			}
			before := len(upstream.sent("eye"))
			for _, suffix := range []string{" ", `{"jsonrpc":"2.0","id":2,"method":"ping"}`} {
				w := post(bounded + suffix)
				if w.Code != http.StatusRequestEntityTooLarge {
					t.Errorf("a %d-byte body exceeds the %d-byte limit: got HTTP %d, want 413; reply %s", len(bounded)+len(suffix), tc.limit, w.Code, w.Body)
				}
			}
			if after := len(upstream.sent("eye")); after != before {
				t.Errorf("oversized bodies must not execute an image call: before %d, after %d", before, after)
			}
		})
	}
}
