package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// An incomplete reply's reason goes to the client as the upstream said it.
// The two reasons the Responses protocol names keep their meanings
// (max_output_tokens is a token limit, content_filter a refusal), and any
// other word — a vendor's own, or none said at all — comes through
// incomplete, never reclassified as a token limit the reply didn't hit
// (#6902, kouekikin24).
func TestResponsesIncompleteReasonKept(t *testing.T) {
	for _, tc := range []struct {
		name, status, reason, want string
	}{
		{"a vendor's own word", "incomplete", "upstream_truncated", "upstream_truncated"},
		{"another vendor's word", "incomplete", "vendor_interrupted", "vendor_interrupted"},
		{"the output cap", "incomplete", "max_output_tokens", "max_output_tokens"},
		{"a refusal", "incomplete", "content_filter", "content_filter"},
		{"no reason said", "incomplete", "", ""},
		{"completed", "completed", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := map[string]any{"id": "resp_fixture", "status": tc.status, "output": []any{}}
			if tc.reason != "" {
				response["incomplete_details"] = map[string]any{"reason": tc.reason}
			}
			terminal, err := json.Marshal(map[string]any{"type": "response." + tc.status, "response": response})
			if err != nil {
				t.Fatal(err)
			}
			var d responsesDecoder
			var col collector
			rec := httptest.NewRecorder()
			enc := &responsesEncoder{w: newSSEWriter(rec), model: "fixture"}
			for _, in := range []string{
				`{"type":"response.created","response":{"id":"resp_fixture","model":"fixture","status":"in_progress"}}`,
				`{"type":"response.output_text.delta","delta":"Partial fixture text"}`,
				string(terminal),
			} {
				if err := d.decode(in, func(e Event) {
					col.add(e)
					enc.event(e)
				}); err != nil {
					t.Fatal(err)
				}
			}
			enc.finish()
			streamed := map[string]any{}
			for _, e := range events(rec.Body.String()) {
				if e["type"] == "response.incomplete" || e["type"] == "response.completed" {
					streamed, _ = e["response"].(map[string]any)
				}
			}
			var whole map[string]any
			if err := json.Unmarshal(renderResponses(col.finish(), "fixture", nil), &whole); err != nil {
				t.Fatal(err)
			}
			for _, out := range []struct {
				name string
				body map[string]any
			}{{"stream", streamed}, {"whole", whole}} {
				t.Run(out.name, func(t *testing.T) {
					if out.body["status"] != tc.status {
						t.Fatalf("status = %v, want %s", out.body["status"], tc.status)
					}
					details, _ := out.body["incomplete_details"].(map[string]any)
					got, _ := details["reason"].(string)
					if tc.want != "" && got != tc.want {
						t.Fatalf("reason = %q, want %q", got, tc.want)
					}
					if tc.want == "" && tc.status == "incomplete" && (got == "max_output_tokens" || got == "content_filter") {
						t.Fatalf("no reason said, yet the client is told %q", got)
					}
				})
			}
		})
	}
}
