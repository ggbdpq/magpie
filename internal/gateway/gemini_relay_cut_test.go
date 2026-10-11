package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A Gemini client's request is relayed as it came (#1346), and a stream of
// its vendor's that stops with no finishReason and no final usageMetadata
// is a reply cut short (#370's Gemini): the client is told so in Gemini's
// own error shape rather than left reading a truncated answer as finished.
// Gemini's chunks name no type and there is no [DONE], so the sniffer
// knows the end by the finishReason a candidate carries or by the
// usageMetadata the final chunk carries — ending on that alone said
// nothing (#667), and is no cut.

func geminiRelay(t *testing.T, chunks ...string) string {
	t.Helper()
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ch := range chunks {
			io.WriteString(w, "data: "+ch+"\n\n")
		}
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "up", Name: "UP", Key: "k", Models: []string{"gemini-2.5-pro"}, Gemini: up.URL + "/v1beta"}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1beta/models/up/gemini-2.5-pro:streamGenerateContent?alt=sse",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestGeminiRelayEndedShort(t *testing.T) {
	body := geminiRelay(t,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"Let me look at the"}]}}]}`)
	if !strings.Contains(body, `"error"`) || !strings.Contains(body, "ended before it was complete") {
		t.Errorf("a cut relay ended as if whole: %s", body)
	}
	// the finishReason said the reply was done
	body = geminiRelay(t,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"Done."}]}}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`)
	if strings.Contains(body, `"error"`) || !strings.Contains(body, `"finishReason":"STOP"`) {
		t.Errorf("a whole relay told as a cut one: %s", body)
	}
	// ending on the usage alone said nothing (#667), but it did end
	body = geminiRelay(t, `{"usageMetadata":{"promptTokenCount":0}}`)
	if strings.Contains(body, `"error"`) {
		t.Errorf("a reply that ended on its usage told as a cut one: %s", body)
	}
}
