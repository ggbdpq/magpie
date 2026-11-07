package usage

import (
	"reflect"
	"testing"
)

// An MCP tool call, a search or a probe days back keeps the settled days:
// its chunk is made again for each query and has no serial, and a key
// that gave up on it paired the whole history on every start and every
// poll past heatFresh, for anyone who called no tool in the last days.
func TestSettledProbePairsRecentOnly(t *testing.T) {
	now, _, paired, restart := pageHistory(t)
	Append(Record{Time: now.AddDate(0, 0, -30), Agent: "codex", Source: SourceMCP, Kind: KindMCP, Requested: "web_search", Status: 200})
	heats := []int{}
	heatPairing = func(n int) { heats = append(heats, n) }
	t.Cleanup(func() { heatPairing = nil })
	page := QueryPage(All, Filter{}, 0, 1)
	heat := HeatmapOf(Filter{})
	if len(*paired) != 1 || len(heats) != 1 {
		t.Fatalf("first: pairings %v (none: the page wasn't split), heat %v", *paired, heats)
	}
	all, heatAll := (*paired)[0], heats[0]
	restart()
	requestCache.Lock()
	requestCache.heatParts, requestCache.heatOld, requestCache.heats = nil, nil, nil
	requestCache.Unlock()
	p, h := len(*paired), len(heats)
	if got := QueryPage(All, Filter{}, 0, 1); len(*paired) != p+1 || (*paired)[p] >= all || !reflect.DeepEqual(got, page) {
		t.Fatalf("started again: paired %v of %d: %+v, was %+v", (*paired)[p:], all, got, page)
	}
	if got := HeatmapOf(Filter{}); len(heats) != h+1 || heats[h] >= heatAll || !reflect.DeepEqual(got, heat) {
		t.Fatalf("started again: the heatmap paired %v of %d", heats[h:], heatAll)
	}
}
