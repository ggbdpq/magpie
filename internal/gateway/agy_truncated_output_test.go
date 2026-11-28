package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

// agy writes a file URI for a tool result it cut short. The file is inside
// the bridge's container, so a client that reads the model's tool result
// cannot open it (#7542). The gateway must drop that link from what it
// sends to the model on an Antigravity turn, and say to ask for a smaller
// result instead. A link a tool wrote itself is not the bridge's note and
// goes through as it came.
func TestAgyTruncatedOutputDropsContainerFileLink(t *testing.T) {
	const accountLink = "The tool's output was truncated. You can find the full output in a file at\n" +
		"file:///config/state/magpie-agy-bridge/accounts/abcdef12/.gemini/antigravity-cli/brain/conv-1/.system_generated/steps/7/output.txt"
	const oldLink = "The tool's output was truncated. You can find the full output in a file at\n" +
		"file:///config/home/.gemini/antigravity-cli/brain/conv-2/.system_generated/steps/3/output.txt"
	const ownLink = "Read file:///work/report.txt"

	for _, tc := range []struct {
		name, agent, text string
		wantLink          bool
	}{
		{"account home", "antigravity", accountLink, false},
		{"old home", "antigravity", oldLink, false},
		{"tool's own link", "antigravity", ownLink, true},
		{"other agent", "gemini", accountLink, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			note, err := json.Marshal(tc.text)
			if err != nil {
				t.Fatal(err)
			}
			body := `{"contents":[` +
				`{"role":"user","parts":[{"text":"hi"}]},` +
				`{"role":"model","parts":[{"functionCall":{"id":"c1","name":"read","args":{}}}]},` +
				`{"role":"user","parts":[{"functionResponse":{"id":"c1","name":"read","response":{"output":` + string(note) + `}}}]}` +
				`]}`
			r, err := parseGemini([]byte(body))
			if err != nil {
				t.Fatal(err)
			}
			built := string(buildCodeAssist(r, "gemini-3-flash", tc.agent))
			got := toolResultOf(t, built)
			if tc.wantLink {
				if got != tc.text {
					t.Fatalf("tool text = %q, want it as it came: %q", got, tc.text)
				}
				return
			}
			if strings.Contains(got, "file:///config/") || strings.Contains(got, "output.txt") {
				t.Fatalf("tool text still names the bridge's container file: %q", got)
			}
			if !strings.Contains(got, "smaller result") {
				t.Fatalf("tool text doesn't say to ask for a smaller result: %q", got)
			}
		})
	}
}

func toolResultOf(t *testing.T, built string) string {
	t.Helper()
	var env struct {
		Request struct {
			Contents []struct {
				Parts []struct {
					Response *struct {
						Response struct {
							Result string `json:"result"`
							Output string `json:"output"`
						} `json:"response"`
					} `json:"functionResponse"`
				} `json:"parts"`
			} `json:"contents"`
		} `json:"request"`
	}
	if err := json.Unmarshal([]byte(built), &env); err != nil {
		t.Fatal(err)
	}
	for _, c := range env.Request.Contents {
		for _, p := range c.Parts {
			if p.Response == nil {
				continue
			}
			if p.Response.Response.Result != "" {
				return p.Response.Response.Result
			}
			if p.Response.Response.Output != "" {
				return p.Response.Response.Output
			}
		}
	}
	t.Fatalf("no tool result in %s", built)
	return ""
}
