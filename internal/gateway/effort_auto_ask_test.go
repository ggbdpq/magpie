package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #2247: a group at effort=auto, its classifier picking low for a turn
// the agent asked high at — what reasons on the wire, and what does the
// trace say of it?

// The classifier's pick is asked for on the wire in place of the level
// the agent asked for, and the try says it was picked.
func TestAutoEffortPickReplacesTheAgentAsk(t *testing.T) {
	s, a, _, j := jevved(t, provider.EffortAuto)
	j.score = 0.31 // low, the report's turn
	out, r := postOK(t, s, "s1", chat("summarise this file", nil, 0, `,"reasoning_effort":"high"`))
	if r.Rule == nil || r.Rule.Pick != "low" || r.Rule.Classified == nil || r.Rule.Classified.Effort != "low" {
		t.Fatalf("rule %+v", r.Rule)
	}
	if sent := sentBody(t, a)["reasoning_effort"]; sent != "low" {
		t.Fatalf("asked high, picked low: sent %v (%s)", sent, out)
	}
	if len(r.Tries) != 1 || !r.Tries[0].Picked || r.Tries[0].Effort != "low" {
		t.Fatalf("tried %+v", r.Tries)
	}
}

// The next turn of the conversation picks low again: taken again, the
// report's turn 19, whose previous pick was low too.
func TestAutoEffortPickAgainNextTurn(t *testing.T) {
	s, a, _, j := jevved(t, provider.EffortAuto)
	j.score = 0.31
	postOK(t, s, "s1", chat("summarise this file", nil, 0, `,"reasoning_effort":"high"`))
	_, r := postOK(t, s, "s1", chat("summarise this file", []string{"and this one"}, 0, `,"reasoning_effort":"high"`))
	if r.Rule == nil || r.Rule.Pick != "low" {
		t.Fatalf("rule %+v", r.Rule)
	}
	if sent := sentBody(t, a)["reasoning_effort"]; sent != "low" {
		t.Fatalf("second turn: sent %v", sent)
	}
}

// A turn whose requests turn reasoning off (effort "none") is not asked
// for a pick: its requests can take none ("only those the agent asked to
// reason"), so the classifier is not called and none is logged — a pick
// beside a none the wire will always carry reads as one the group failed
// to apply (#2247).
func TestAutoEffortNoneAsk(t *testing.T) {
	s, a, _, j := jevved(t, provider.EffortAuto)
	j.score = 0.31
	out, r := postOK(t, s, "s2", chat("list the files", nil, 0, `,"reasoning_effort":"none"`))
	if sent := sentBody(t, a)["reasoning_effort"]; sent != "none" {
		t.Fatalf("asked none: sent %v (%s)", sent, out)
	}
	if n := len(j.turns()); n != 0 {
		t.Fatalf("the classifier was asked %d time(s) for a turn that asked no reasoning", n)
	}
	if r.Rule == nil || r.Rule.Classified != nil || r.Rule.Pick != "" {
		t.Fatalf("a pick was logged for a none ask: %+v", r.Rule)
	}
}

// The same on the Responses route, whose none rides in reasoning.effort.
func TestAutoEffortNoneAskResponses(t *testing.T) {
	s, _, _, j := jevved(t, provider.EffortAuto)
	j.score = 0.31
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"group/r","stream":false,"input":"hi","reasoning":{"effort":"none"}}`)))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if n := len(j.turns()); n != 0 {
		t.Fatalf("the classifier was asked %d time(s) for a turn that asked no reasoning", n)
	}
	if r := lastRoute(s); r.Rule == nil || r.Rule.Pick != "" {
		t.Fatalf("a pick was logged for a none ask: %+v", r.Rule)
	}
}
