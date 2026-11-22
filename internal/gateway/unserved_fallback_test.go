package gateway

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// modelscopeNoProvider400 is ModelScope's answer to a model id none of its
// instances serves, as the reporter of #2887's usage.jsonl has it: the
// request itself is fine, and a member that serves the model may answer it.
const modelscopeNoProvider400 = `{"error":{"message":"Model id : deepseek-ai/DeepSeek-V4.1-Flash , has no provider supported"}}`

// A member whose vendor serves the model nowhere hands the request to the
// group's next one, and the model rests on that key alone, as any unserved
// model's does — so the client's own retry, landing on the group again, is
// no longer sent to the dead seat first (#2887: 28 requests in a row hit
// the first member and the later ones were never asked).
func TestGroupFailsOverAModelWithNoProvider(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{400, "", modelscopeNoProvider400}}}
	b := &scripted{replies: []reply{{200, "", chatOK}}}
	scriptedOn(t, "ms", provider.Chat, a)
	scriptedOn(t, "b", provider.Chat, b)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"ms/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "hello") || a.n != 1 || b.n != 1 {
		t.Fatalf("%d %s (ms %d, b %d)", code, body, a.n, b.n)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 2 || r.Tries[0].Status != 400 || r.Tries[0].Fail != failOther {
		t.Fatalf("tries %+v", r.Tries)
	}
	if rest := r.Tries[0].Rest; rest == nil || rest.Key != "ms/m" {
		t.Fatalf("the model alone was to rest: %+v", r.Tries[0].Rest)
	}

	// the rested seat is passed over: the next group request, as the
	// client's retry is, is answered by the second member alone
	code, body = postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "hello") || a.n != 1 || b.n != 2 {
		t.Fatalf("next: %d %s (ms %d, b %d)", code, body, a.n, b.n)
	}

	for _, x := range []struct {
		status int
		body   string
		want   bool
	}{
		{400, modelscopeNoProvider400, true},
		// as magpie tells it on, with the provider's name before, and
		// ModelScope's own invalid_request_error type around it
		{400, `ModelScope: ` + modelscopeNoProvider400, true},
		{400, `{"error":{"message":"Model id : deepseek-ai/DeepSeek-V4.1-Flash , has no provider supported","type":"invalid_request_error"}}`, true},
		// Azure OpenAI's error for a deployment its resource has none of
		// (DeploymentNotFound, "The API deployment for this resource does
		// not exist"): a 404 fails over by its status already, a relay's
		// 400 says the words
		{400, `{"error":{"code":"DeploymentNotFound","message":"The API deployment for this resource does not exist. If you created the deployment recently, please wait a few minutes and try again."}}`, true},
		{400, `{"error":{"message":"Deployment not found for ModelName: gpt-4o"}}`, true},
		// model_not_found fails over as it did
		{400, `{"error":{"message":"The model gpt-x does not exist","code":"model_not_found"}}`, true},
		// a request at fault stays the agent's: no member is asked after
		{400, `{"error":{"message":"Invalid parameter: messages.3.content"}}`, false},
		{400, `{"error":{"message":"messages: field required"}}`, false},
	} {
		if got := retryable(x.status, []byte(x.body)); got != x.want {
			t.Errorf("retryable(%d, %s) = %v", x.status, x.body, got)
		}
		if got := shapeRefused(x.status, []byte(x.body)); got {
			t.Errorf("shapeRefused(%d, %s) = %v; an unserved model rests, it is not a shape", x.status, x.body, got)
		}
	}
	if !modelRefused(400, []byte(modelscopeNoProvider400)) {
		t.Errorf("modelRefused false: the model wouldn't rest alone on its key")
	}
}

// magpieSwitchedOff404 is what a remote magpie member answers when the
// provider that would serve the model there is switched off (#7503):
// magpie's own switchedOff wording in a 404. It says "is not served" —
// words unservedWords matched nowhere, so the member was read as any
// unknown failure and the group asked it again on every request.
const magpieSwitchedOff404 = `{"error":{"message":"ZCode is switched off in Magpie, so \"GLM-5.3-Flash-Trial\" is not served; switch it on again in Magpie's Providers to use it","type":"not_found_error"}}`

// A member whose magpie has the model's provider switched off answers
// that 404 for as long as it stays off: as any unserved model's, it hands
// the request to the group's next member and rests for that model alone,
// so the client's next request, landing on the group again, is not sent
// to the dead seat first (#7503).
func TestGroupFailsOverASwitchedOffMember(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{404, "", magpieSwitchedOff404}}}
	b := &scripted{replies: []reply{{200, "", chatOK}}}
	scriptedOn(t, "school", provider.Chat, a)
	scriptedOn(t, "b", provider.Chat, b)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"school/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "hello") || a.n != 1 || b.n != 1 {
		t.Fatalf("%d %s (school %d, b %d)", code, body, a.n, b.n)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 2 || r.Tries[0].Status != 404 || r.Tries[0].Fail != failOther {
		t.Fatalf("tries %+v", r.Tries)
	}
	if rest := r.Tries[0].Rest; rest == nil || rest.Key != "school/m" || rest.Model != "m" {
		t.Fatalf("the model alone was to rest on the member: %+v", r.Tries[0].Rest)
	}

	// the rested seat is passed over: the next group request, as the
	// client's retry is, is answered by the second member alone
	code, body = postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "hello") || a.n != 1 || b.n != 2 {
		t.Fatalf("next: %d %s (school %d, b %d)", code, body, a.n, b.n)
	}

	// magpie's own wording, the member's label before it or not, is an
	// unserved model's refusal, not the request's or a shape's
	for _, x := range []struct {
		status int
		body   string
		want   bool
	}{
		{404, magpieSwitchedOff404, true},
		{404, `school: ZCode is switched off in Magpie, so "GLM-5.3-Flash-Trial" is not served; switch it on again in Magpie's Providers to use it`, true},
	} {
		if got := modelRefused(x.status, []byte(x.body)); got != x.want {
			t.Errorf("modelRefused(%d, %s) = %v", x.status, x.body, got)
		}
		if got := shapeRefused(x.status, []byte(x.body)); got {
			t.Errorf("shapeRefused(%d, %s) = %v; an unserved model rests, it is not a shape", x.status, x.body, got)
		}
	}
}
