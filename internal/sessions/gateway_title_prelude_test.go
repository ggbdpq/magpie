package sessions

import (
	"strings"
	"testing"
	"time"
)

// A gateway turn whose system prompt and tool schema fill the 64 KiB input
// budget must still yield the user's first question as the session title.
// The context parts are stored first, so bound() must not let them crowd out
// the user text that firstAsked needs (#6541).
func TestGatewayTitleSurvivesLargePrelude(t *testing.T) {
	gatewayHome(t)
	if err := SetGatewayRecording(true, false); err != nil {
		t.Fatal(err)
	}
	asked := "Deploy the staging server with the new config"
	turn := GatewayTurn{
		Agent: "pi", Session: "s", Time: time.Now().UTC(),
		Input: []Part{
			{Role: "system", Kind: "context", Name: "System prompt", Text: strings.Repeat("s", 40000)},
			{Role: "system", Kind: "context", Name: "Tools", Text: strings.Repeat("t", 40000)},
			{Role: "user", Kind: "text", Text: asked},
		},
	}
	if err := SaveGatewayTurn(turn); err != nil {
		t.Fatal(err)
	}
	titles := GatewayTitler()
	if got := titles("pi", "s"); got != asked {
		t.Fatalf("gateway title lost when system+tools fill the budget: got %q, want %q", got, asked)
	}
}
