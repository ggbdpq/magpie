package gateway

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// autodlShape400 is AutoDL's relay 400 to DeepSeek's server-side tool
// {"type":"deepseek_web_search"} (#7209): a param value the relay's API
// doesn't accept, which the next group member (DeepSeek's own) serves.
const autodlShape400 = "AutoDL: [invalid_params]: The parameter `tools.type` specified in the request are not valid: invalid value: `deepseek_web_search`, value must be `function`."

// A relay re-exposing a vendor's model refuses a server-side tool type that
// only the vendor's own API serves. It is a request-shape refusal: the next
// group member is asked, and the relay doesn't rest. Before this, the 400
// went straight to the agent and the official member was never asked.
func TestGroupFailsOverARelaysServerToolRefusal(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{400, "", autodlShape400}}}
	b := &scripted{replies: []reply{{200, "", chatOK}}}
	scriptedOn(t, "autodl", provider.Chat, a)
	scriptedOn(t, "deepseek", provider.Chat, b)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"autodl/m", "deepseek/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"deepseek_web_search"}]}`)
	if code != 200 || !strings.Contains(body, "hello") || a.n != 1 || b.n != 1 {
		t.Fatalf("%d %s (autodl %d, deepseek %d)", code, body, a.n, b.n)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 2 || r.Tries[0].Status != 400 || r.Tries[0].Fail != failShape || r.Tries[0].Rest != nil {
		t.Fatalf("tries %+v", r.Tries)
	}

	for _, x := range []struct {
		status int
		body   string
		want   bool
	}{
		{400, autodlShape400, true},
		{400, "AutoDL: [invalid_params]: quota exhausted, value must be `function`", false},
		{400, "AutoDL: [invalid_params]: invalid api key, value must be `function`", false},
	} {
		if got := shapeRefused(x.status, []byte(x.body)); got != x.want {
			t.Errorf("shapeRefused(%d, %s) = %v, want %v", x.status, x.body, got, x.want)
		}
	}
}
