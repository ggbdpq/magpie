package gateway

import (
	"bytes"
	"encoding/json"
	"github.com/yetone/magpie/internal/gwmw"
	"github.com/yetone/magpie/internal/middleware"
	"github.com/yetone/magpie/internal/provider"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

const compactSignatureHistory = `{"model":"fake/m1","stream":true,"input":[{"type":"message","role":"user","content":"Synthetic calculator fixture."},{"type":"function_call","id":"fc_fixture","call_id":"call_fixture","name":"read_fixture","arguments":"{\"extra_content\":\"business data\"}","extra_content":{"google":{"thought_signature":"synthetic-signature"}}},{"type":"function_call_output","call_id":"call_fixture","output":"synthetic tool result"},{"type":"compaction_trigger"}]}`

func compactCleaner(t *testing.T) {
	t.Helper()
	src, err := os.ReadFile("testdata/codex-compaction-clean.middleware.mjs")
	if err != nil {
		t.Fatal(err)
	}
	onMiddleware(t, string(src))
	stop := gwmw.Use(middleware.Chain{})
	t.Cleanup(stop)
}

func TestCompactionMiddlewareOfficialSummaryCopy(t *testing.T) {
	up := setup(t, provider.Responses, &fake{t: t})
	var mu sync.Mutex
	var sent [][]byte
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			io.WriteString(w, `{"models":[]}`)
			return
		}
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		sent = append(sent, append([]byte(nil), b...))
		mu.Unlock()
		if bytes.Contains(b, []byte(`"extra_content":{"google"`)) {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"Unknown parameter: input[1].extra_content","type":"invalid_request_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, summaryReply)
	})
	codexSignedIn(t)
	if err := provider.Save(provider.Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "fixture-key", Models: []string{"m1"}, Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	compactCleaner(t)
	if err := provider.SetCodexCompactModel("codex/gpt-5.5"); err != nil {
		t.Fatal(err)
	}
	original := []byte(compactSignatureHistory)
	before := append([]byte(nil), original...)
	code, reply := codexPost(t, string(original))
	if code != 200 || !strings.Contains(compactedText(t, reply), "SUMMARY") {
		t.Fatalf("summary status=%d reply=%s", code, reply)
	}
	if !bytes.Equal(original, before) {
		t.Fatal("source history changed")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 1 || bytes.Contains(sent[0], []byte(`"extra_content":{"google"`)) {
		t.Fatalf("official copy was not cleaned; calls=%d", len(sent))
	}
	if !bytes.Contains(sent[0], []byte(`business data`)) || !bytes.Contains(original, []byte(`synthetic-signature`)) {
		t.Fatal("business argument or original signature lost")
	}
}

func TestCompactionMiddlewareNativeCompactCopy(t *testing.T) {
	setup(t, provider.Responses, &fake{t: t})
	var sent []byte
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		if bytes.Contains(sent, []byte(`"extra_content":{"google"`)) {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"Unknown parameter: extra_content"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"resp_compact","object":"response.compaction","output":[{"type":"compaction","encrypted_content":"synthetic-seal"}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}`)
	})
	compactCleaner(t)
	body := strings.Replace(compactSignatureHistory, `"fake/m1"`, `"gpt-5.5"`, 1)
	body = strings.Replace(body, `,{"type":"compaction_trigger"}`, "", 1)
	req := httptest.NewRequest("POST", CodexPath+"/responses/compact", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("chatgpt-account-id", "acct-1")
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || bytes.Contains(sent, []byte(`"extra_content":{"google"`)) {
		t.Fatalf("native status=%d reply=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(body, "synthetic-signature") {
		t.Fatal("original signature lost")
	}
}

func TestCompactionMiddlewareOtherProviderUntouched(t *testing.T) {
	f := &fake{t: t, reply: summaryReply}
	setup(t, provider.Responses, f)
	compactCleaner(t)
	code, reply := codexPost(t, compactSignatureHistory)
	if code != 200 {
		t.Fatalf("status=%d reply=%s", code, reply)
	}
	if !strings.Contains(string(f.got), "synthetic-signature") {
		t.Fatal("another provider lost its signature")
	}
}

// A successful summary is not enough: the same retained history must still
// carry its vendor signature on a later ordinary tool continuation.
func TestCompactionMiddlewareRetainedHistoryContinuesWithSignature(t *testing.T) {
	f := &fake{t: t, reply: summaryReply}
	setup(t, provider.Responses, f)
	compactCleaner(t)
	code, reply := codexPost(t, compactSignatureHistory)
	if code != 200 || !strings.Contains(compactedText(t, reply), "SUMMARY") {
		t.Fatalf("compaction status=%d reply=%s", code, reply)
	}
	var request map[string]json.RawMessage
	if err := json.Unmarshal([]byte(compactSignatureHistory), &request); err != nil {
		t.Fatal(err)
	}
	var input []json.RawMessage
	if err := json.Unmarshal(request["input"], &input); err != nil {
		t.Fatal(err)
	}
	// Remove only the trigger, retaining the signed tool exchange verbatim.
	input = input[:len(input)-1]
	request["input"], _ = json.Marshal(input)
	// Caller metadata must not grant an ordinary request compact privileges.
	request["kind"] = json.RawMessage(`"compact"`)
	body, _ := json.Marshal(request)
	code, reply = codexPost(t, string(body))
	if code != 200 {
		t.Fatalf("continuation status=%d reply=%s", code, reply)
	}
	if !strings.Contains(string(f.got), `"thought_signature":"synthetic-signature"`) ||
		!strings.Contains(string(f.got), `"call_id":"call_fixture"`) ||
		!strings.Contains(string(f.got), `synthetic tool result`) {
		t.Fatal("ordinary continuation lost its original signature, call ID or tool result")
	}
}
