package gateway

import (
	"net/http"
	"strings"
	"testing"
)

// A plugin's ctx.session is the session the client declared: X-Magpie-Session
// when it names one, else one of the headers agents already send
// (sessionHeaders), and "" when the request names none — magpie never
// invents one from the body (#5192). A generic OpenAI-compatible client
// that sends no session header is no session, and a hook that writes
// per-conversation state has to degrade on that.
func TestSessionOfTheHookContext(t *testing.T) {
	if got := sessionOf(http.Header{}); got != "" {
		t.Errorf("no header: %q", got)
	}
	if got := sessionOf(http.Header{"User-Agent": {"cindy/1.0"}}); got != "" {
		t.Errorf("no session header: %q", got)
	}
	for _, h := range append([]string{SessionHeader}, sessionHeaders...) {
		head := http.Header{}
		head.Set(h, "s-1")
		if got := sessionOf(head); got != "s-1" {
			t.Errorf("%s: %q", h, got)
		}
	}
	head := http.Header{"X-Claude-Code-Session-Id": {"agents"}}
	head.Set("X-Magpie-Session", "mine")
	if got := sessionOf(head); got != "mine" {
		t.Errorf("X-Magpie-Session did not stand in front: %q", got)
	}
	if got := sessionOf(http.Header{"X-Session-Id": {"  s-2  "}}); got != "s-2" {
		t.Errorf("untrimmed: %q", got)
	}
	if got := sessionOf(http.Header{"X-Session-Id": {strings.Repeat("s", 200)}}); len(got) != 128 {
		t.Errorf("cap: %d bytes", len(got))
	}
}
