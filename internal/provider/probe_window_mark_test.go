package provider

import (
	"strings"
	"testing"
)

// The endpoint test probe must send the vendor's model id, not magpie's
// window mark. A picked model id can carry Claude Code's "[1m]" (or the
// catalog's "[1M]") suffix; the gateway strips it in Resolve before asking
// upstream, but the probe built its own body from the stored id and sent the
// marked name, which vendors such as DeepSeek answer with 400 (#6826).
func TestProbeStripsWindowMark(t *testing.T) {
	q := Provider{Preset: "deepseek", Chat: "https://api.deepseek.com/v1"}
	for _, marked := range []string{"deepseek-flash[1m]", "deepseek-flash[1M]"} {
		_, body := tiny(q, Chat, marked)
		if strings.Contains(body, `"model":"`+marked+`"`) {
			t.Fatalf("probe sent the window mark upstream for %q: %s", marked, body)
		}
		if !strings.Contains(body, `"model":"deepseek-flash"`) {
			t.Fatalf("probe did not send the bare model for %q: %s", marked, body)
		}
	}
}
