package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// A Responses stream's error event carries its code and param on itself,
// as the documented one does. A client of the Responses API has them as
// the upstream said them; one of Chat, translated, has them beside the
// message in its error, not the type alone (#3427).
func TestResponsesFlatErrorDiagnostics(t *testing.T) {
	for _, fixture := range []struct {
		name, code, message string
		param               any
	}{
		{"documented_example", "ERR_SOMETHING", "Something went wrong", nil},
		{"parameter", "invalid_prompt", "The input is invalid.", "input"},
	} {
		for _, chat := range []bool{false, true} {
			route := "responses"
			if chat {
				route = "chat"
			}
			t.Run(fixture.name+"/"+route, func(t *testing.T) {
				data, _ := json.Marshal(map[string]any{
					"type": "error", "sequence_number": 1,
					"code": fixture.code, "message": fixture.message, "param": fixture.param,
				})
				setup(t, provider.Responses, &fake{t: t, reply: "event: error\ndata: " + string(data) + "\n\n"})
				endpoint, body := "/v1/responses", `{"model":"fake/m1","stream":true,"input":"Hello."}`
				if chat {
					endpoint, body = "/v1/chat/completions", `{"model":"fake/m1","stream":true,"messages":[{"role":"user","content":"Hello."}]}`
				}
				rec := httptest.NewRecorder()
				New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", endpoint, strings.NewReader(body)))
				var failure gjson.Result
				if err := readSSE(strings.NewReader(rec.Body.String()), func(_, data string) error {
					e := gjson.Parse(data)
					if e.Get("error").IsObject() {
						failure = e.Get("error")
					} else if e.Get("type").String() == "error" {
						failure = e
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if failure.Get("message").String() != fixture.message {
					t.Fatalf("error message missing: %s", rec.Body)
				}
				if failure.Get("code").String() != fixture.code {
					t.Errorf("code lost: %s", failure.Raw)
				}
				if fixture.param != nil && failure.Get("param").String() != fixture.param {
					t.Errorf("param lost: %s", failure.Raw)
				}
			})
		}
	}
}
