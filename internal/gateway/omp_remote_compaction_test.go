package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #5065: omp with remoteCompaction enabled sends a compaction_trigger on
// /v1/responses. When it can't be carried — the model's provider is not set
// to compact natively — the answer is an error, never the "magpie1:"
// compaction item codexCompact writes: omp's V2 check takes that for a
// successful seal and never falls back, so the conversation would go on
// from magpie's summary unasked.
func TestOmpTriggerUncarriedIsAnError(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"SUMMARY"}]}}`,
		`data: {"type":"response.completed","response":{"id":"resp_s","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)}
	setup(t, provider.Responses, f)
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("omp's compaction reached OpenAI")
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(
		`{"model":"fake/m1","stream":true,"input":[{"type":"compaction_trigger"},{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the bug"}]}]}`))
	req.Header.Set("Authorization", "Bearer "+TokenFor("omp"))
	New().Handler().ServeHTTP(rec, req)
	if rec.Code == 200 || strings.Contains(rec.Body.String(), magpieCompaction) {
		t.Fatalf("omp's trigger answered %d %s", rec.Code, rec.Body.String())
	}
	if f.calls != 0 {
		t.Errorf("the summary was asked of the provider anyway (%d calls)", f.calls)
	}
}
