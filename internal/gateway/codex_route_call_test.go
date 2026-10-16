package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A request the Codex backend turned away — an API key OpenAI refuses
// (401), or an error it said nothing of — is one of the Routing page's
// requests (its trace begins in codexUpstreamOn), and the call it records
// names the route (Call.Route), as serve's, the Codex backend's answered
// one and a refused title's do: the request opens to its bodies while the
// ring still has them.
func TestCodexTurnedAwayCallNamesItsRoute(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	for _, tc := range []struct {
		name   string
		ask    func(w http.ResponseWriter)
		status int
	}{
		{"an api key openai refuses", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":{"message":"Incorrect API key provided: sk-A2sz0****owqK.","code":"invalid_api_key"}}`)
		}, http.StatusUnauthorized},
		{"an error that says nothing", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusInternalServerError)
		}, http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tc.ask(w)
			}))
			t.Cleanup(up.Close)
			was := codexAPIBase
			codexAPIBase = up.URL
			t.Cleanup(func() { codexAPIBase = was })
			s := New()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-6-astra","input":"hi","stream":true}`))
			req.Header.Set("Authorization", "Bearer sk-relay-key")
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("gateway: %d %.200s", rec.Code, rec.Body)
			}
			routes := s.Trace(context.Background(), 0, 0).Routes
			if len(routes) != 1 {
				t.Fatalf("%d routes, want 1", len(routes))
			}
			c, ok := s.RouteCall(routes[0].ID)
			if !ok {
				t.Fatalf("route %d: the ring has no call for it", routes[0].ID)
			}
			if c.Status != tc.status {
				t.Fatalf("route %d's call: %d, want %d", routes[0].ID, c.Status, tc.status)
			}
		})
	}
}
