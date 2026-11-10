package gateway

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// This is the refusal recorded by a relay serving Azure OpenAI resources.
const azureResourceError = `{"error":{"message":"The requested item was created under a different Azure OpenAI resource. Use the same resource that created the item to access it.","type":"invalid_request_error"}}`

const azureResourceAnswer = `{"id":"resp_resource","object":"response","status":"completed","output":[{"type":"message","id":"msg_resource","role":"assistant","content":[{"type":"output_text","text":"answer"}]}],"usage":{"input_tokens":3,"output_tokens":1}}`

// Codex sends its own full history with store:false, so there may be no
// stored conversation to repair. A repair that still fails has the same
// outcome: another member answers, and later requests prefer that member
// while the failing relay rests.
func TestAzureResourceMismatchFailsOverAndRests(t *testing.T) {
	for _, held := range []bool{false, true} {
		t.Run(fmt.Sprintf("stored_history=%t", held), func(t *testing.T) {
			fresh(t)
			up := newPrevUp(t, func(n int, _ []byte) (int, string, string) {
				if held && n == 1 {
					return http.StatusOK, "application/json", azureResourceAnswer
				}
				return http.StatusBadRequest, "application/json", azureResourceError
			})
			spare := newPrevUp(t, func(_ int, _ []byte) (int, string, string) {
				return http.StatusOK, "application/json", azureResourceAnswer
			})
			s := prevSandbox(t, provider.Provider{ID: "azure", Name: "Azure relay", Key: "k1", Models: []string{"m1"}, Responses: up.srv.URL + "/v1", RestCap: 720})
			if err := provider.Save(provider.Provider{ID: "spare", Name: "Spare", Key: "k2", Models: []string{"m1"}, Responses: spare.srv.URL + "/v1"}); err != nil {
				t.Fatal(err)
			}
			if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"azure/m1", "spare/m1"}, Routing: provider.Ordered, Affinity: provider.AffinityOff}); err != nil {
				t.Fatal(err)
			}
			body := `{"model":"group/g","store":false,"input":[{"role":"user","content":"continue"}]}`
			wantCalls := 1
			if held {
				id := responseID(t, askResponses(t, s, "", `{"model":"azure/m1","input":"first turn"}`))
				body = `{"model":"group/g","previous_response_id":"` + id + `","input":"second turn"}`
				wantCalls = 3 // first turn, then the original and repaired requests
			}
			rec := askResponses(t, s, "", body)
			if rec.Code != http.StatusOK || spare.calls() != 1 || up.calls() != wantCalls {
				t.Fatalf("client got %d, relay calls %d, spare calls %d: %s", rec.Code, up.calls(), spare.calls(), rec.Body.String())
			}
			route := lastRoute(s)
			if len(route.Tries) != 2 || route.Tries[0].ID != "azure" || route.Tries[0].Status != http.StatusBadRequest || route.Tries[0].Rest == nil || route.Tries[1].ID != "spare" {
				t.Fatalf("resource refusal did not rest and fall back: %+v", route.Tries)
			}
			rest := route.Tries[0].Rest
			if rest.By != "backoff" || !rest.Until.After(time.Now()) || !strings.Contains(rest.Error, "different Azure OpenAI resource") {
				t.Fatalf("missing cooldown or original error: %+v", rest)
			}
			rec = askResponses(t, s, "", `{"model":"group/g","store":false,"input":"another request"}`)
			if rec.Code != http.StatusOK || up.calls() != wantCalls || spare.calls() != 2 {
				t.Fatalf("resting relay asked again before spare: status %d, relay %d, spare %d", rec.Code, up.calls(), spare.calls())
			}
		})
	}
}

// A normal client-input 400 must still reach the client without routing
// the same invalid request to every provider or resting working keys.
func TestAzureResourceMismatchLeavesOtherBadRequestsAlone(t *testing.T) {
	fresh(t)
	up := newPrevUp(t, func(_ int, _ []byte) (int, string, string) {
		return http.StatusBadRequest, "application/json", `{"error":{"message":"messages: field required"}}`
	})
	spare := newPrevUp(t, func(_ int, _ []byte) (int, string, string) {
		return http.StatusOK, "application/json", azureResourceAnswer
	})
	s := prevSandbox(t, provider.Provider{ID: "azure", Name: "Azure relay", Key: "k1", Models: []string{"m1"}, Responses: up.srv.URL + "/v1"})
	if err := provider.Save(provider.Provider{ID: "spare", Name: "Spare", Key: "k2", Models: []string{"m1"}, Responses: spare.srv.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"azure/m1", "spare/m1"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	rec := askResponses(t, s, "", `{"model":"group/g","store":false,"input":"hi"}`)
	route := lastRoute(s)
	if rec.Code != http.StatusBadRequest || up.calls() != 1 || spare.calls() != 0 || len(route.Tries) != 1 || route.Tries[0].Rest != nil {
		t.Fatalf("ordinary bad request retried or rested: status %d, relay %d, spare %d, tries %+v", rec.Code, up.calls(), spare.calls(), route.Tries)
	}
}
