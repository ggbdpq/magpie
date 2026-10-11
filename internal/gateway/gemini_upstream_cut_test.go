package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A Gemini upstream (a custom provider's Gemini URL, #1346, or Vertex AI)
// whose stream stops with no finishReason is a reply cut short, as an
// Anthropic one with no stop_reason and no message_stop or a Responses one
// with no response.completed is (#1562, #1563): the client is told so
// rather than handed a truncated answer presented as finished. Gemini's
// chunks name no type, so their last one is known by the finishReason it
// carries.

// relayGemini serves a translated request's model from a Gemini upstream
// answering with chunks, as they are. stream says whether the client asked
// for its reply streamed.
func relayGemini(t *testing.T, stream bool, chunks ...string) (int, string) {
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
	return post(t, "/v1/chat/completions", `{"model":"up/gemini-2.5-pro","stream":`+boolWord(stream)+`,"messages":[{"role":"user","content":"hi"}]}`)
}

func boolWord(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func TestGeminiUpstreamEndedShort(t *testing.T) {
	code, body := relayGemini(t, true,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"Let me look at the"}]}}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if !strings.Contains(body, "ended before it was complete") {
		t.Errorf("a cut reply finished as whole: %s", body)
	}
	// the chunk with the finishReason said the reply was done
	code, body = relayGemini(t, true,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"Done."}]}}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := chatFinish(body); got != "stop" || strings.Contains(body, "ended before") {
		t.Errorf("finish_reason %q: %s", got, body)
	}
	// Code Assist's chunks wrap the candidates in a response, and their
	// finishReason ends the reply the same way
	code, body = relayGemini(t, true,
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Done."}]}}]}}`,
		`{"response":{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := chatFinish(body); got != "stop" || strings.Contains(body, "ended before") {
		t.Errorf("finish_reason %q: %s", got, body)
	}
}

// A cut reply reaches a client that asked for no stream as the error too,
// not as the whole answer the cut masquerades as.
func TestGeminiNonStreamEndedShort(t *testing.T) {
	code, body := relayGemini(t, false,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"Let me look at the"}]}}]}`)
	if code != 502 || !strings.Contains(body, "ended before it was complete") {
		t.Errorf("a cut reply served whole (status %d): %s", code, body)
	}
	code, body = relayGemini(t, false,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"Done."}]}}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`)
	if code != 200 || !strings.Contains(body, "Done.") || strings.Contains(body, "ended before") {
		t.Errorf("status %d: %s", code, body)
	}
}
