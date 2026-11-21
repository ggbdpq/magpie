package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A vendor that says whether the agent should ask again (X-Should-Retry,
// false beside Anthropic's own errors, true beside some it would take
// later) is heard when magpie rebuilds the error for the agent: the word
// rides keepRetry onto the rebuilt reply, and the routing loop's rebuilt
// errors keep the try's word. A rebuild that loses it has the client ask
// a refusal again for good — a plugin declining an oversized compaction
// with 400 compact_declined and x-should-retry: false was retried so
// (#7189, Gahyu96's review).
func TestRebuiltErrorKeepsShouldRetry(t *testing.T) {
	serving := func(t *testing.T, id string, status int, retry string) {
		t.Helper()
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "application/json")
			if retry != "" {
				w.Header().Set("X-Should-Retry", retry)
			}
			w.WriteHeader(status)
			io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"compact_declined: the rendered prompt is past the bridge's prompt cap"}}`)
		}))
		if err := provider.Save(provider.Provider{ID: id, Name: "Declining", Key: "k", Chat: up.URL + "/v1", Models: []string{"m"}}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(up.Close)
	}
	post := func(model string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`))
		rr := httptest.NewRecorder()
		New().Handler().ServeHTTP(rr, rec)
		return rr
	}
	for _, tc := range []struct {
		name          string
		id            string
		status        int
		retry, worked string
	}{
		{name: "a false is kept", id: "declining", status: http.StatusBadRequest, retry: "false", worked: "false"},
		{name: "a true is kept", id: "declining", status: http.StatusInternalServerError, retry: "true", worked: "true"},
		{name: "nothing said, nothing added", id: "declining", status: http.StatusBadRequest, worked: ""},
	} {
		fresh(t)
		serving(t, tc.id, tc.status, tc.retry)
		rr := post(tc.id + "/m")
		if rr.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.name, rr.Code, rr.Body.String())
		}
		if got := rr.Header().Get("X-Should-Retry"); got != tc.worked {
			t.Errorf("%s: the rebuilt error says X-Should-Retry %q, want %q", tc.name, got, tc.worked)
		}
	}
}

// The routing loop's rebuilt errors keep the try's word too: a group's
// last member answering 413 with x-should-retry: false has the agent told
// a rebuilt 413, which keeps the false (#7189 review).
func TestRebuiltLoopErrorKeepsShouldRetry(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	asks := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		asks++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Should-Retry", "false")
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"Request Entity Too Large"}}`)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "big", Name: "Big", Key: "k", Chat: up.URL + "/v1", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"big/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	rr := httptest.NewRecorder()
	New().Handler().ServeHTTP(rr, rec)
	mu.Lock()
	n := asks
	mu.Unlock()
	if n == 0 {
		t.Fatal("the vendor was asked nothing")
	}
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("the decline: %d %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-Should-Retry"); got != "false" {
		t.Errorf("the rebuilt error says X-Should-Retry %q, want false", got)
	}
}

// A plugin declining an oversized compaction with 400 compact_declined and
// x-should-retry: false (#7189, the companion plugin's own refusal): the
// agent is told a rebuilt error, which keeps the false.
func TestPluginDeclineKeepsShouldRetry(t *testing.T) {
	pid := besideFake(t, "fakeco", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Should-Retry", "false")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"compact_declined: rendered prompt past the bridge's prompt cap"}}`)
	}))
	rec := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"`+pid+`/fake-claude","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	rr := httptest.NewRecorder()
	New().Handler().ServeHTTP(rr, rec)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("the decline: %d %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-Should-Retry"); got != "false" {
		t.Errorf("the rebuilt error says X-Should-Retry %q, want false", got)
	}
}
