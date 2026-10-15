package agent

import (
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// #2380: a model that thinks with no levels to pick (Trae CN's Kimi-K3, a
// thinking switch alone) went into Pi's models.json as reasoning false,
// where the gateway's /v1/models says true of the same entry, and Pi's
// Reasoning effort slider was disabled for it. Pi's entry says what the
// gateway's does: a model that thinks says so, levels or not, and one
// known not to think doesn't.
func TestPiReasoningSaysWhatTheGatewayDoes(t *testing.T) {
	for _, c := range []struct {
		m    catalog.Model
		want bool
	}{
		// thinks, no levels: the gateway's e.Reasoning alone
		{catalog.Model{ID: "trae-cn/kimi-k3", Name: "Kimi K3", Reasoning: true}, true},
		// thinks with levels, as before
		{catalog.Model{ID: "relay/glm-4.6", Name: "GLM-4.6", Efforts: []string{"low", "high"}}, true},
		// both
		{catalog.Model{ID: "x/m", Reasoning: true, Efforts: []string{"high"}}, true},
		// known not to think
		{catalog.Model{ID: "relay/fast", Name: "Fast"}, false},
	} {
		e := piModelJSON(c.m, "http://127.0.0.1:1", true)
		if got := e["reasoning"]; got != c.want {
			t.Errorf("%s: reasoning %v, want %v", c.m.ID, got, c.want)
		}
	}
}
