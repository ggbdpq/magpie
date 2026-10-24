package catalog

import "testing"

// Copilot bills a Claude model's 1-hour cache write at its 5-minute price
// and its list names no 1-hour one (mdwsk88 on #3435: claude-opus-5.5's
// writes are $5 per million whatever their TTL): the 2× input the fill
// gives a Claude model is Anthropic's own charge, so a price of another
// provider's stands as that provider lists it, while the maker's own entry
// keeps it — it is what a bare id, a relay with no price of its own, and a
// Claude account are billed at.
func TestOneHourWriteScopedToAnthropic(t *testing.T) {
	writeCatalog(t, `{"copilot":{"id":"copilot","models":{"claude-opus-5-5":{"id":"claude-opus-5-5","cost":{
		"input":4,"output":20,"cache_read":0.2,"cache_write":5}}}},
	"anthropic":{"id":"anthropic","models":{"claude-opus-5-5":{"id":"claude-opus-5-5","cost":{
		"input":5,"output":25,"cache_read":0.5,"cache_write":6.25}}}}}`)
	c, _ := PriceOf("anthropic", "claude-opus-5-5")
	if c.CacheWrite1h != 10 {
		t.Errorf("anthropic 1h = %v, want 10 (Anthropic's own 2×)", c.CacheWrite1h)
	}
	p, _ := PriceOf("copilot", "claude-opus-5-5")
	if p.CacheWrite1h != 0 {
		t.Errorf("copilot 1h = %v, want none: its list gives no 1-hour price", p.CacheWrite1h)
	}
	if got := p.OneHour(); got != 5 {
		t.Errorf("copilot OneHour = %v, want its 5-minute price 5", got)
	}
	// the issue's third request at Copilot's own prices: input 2, output 4,
	// 15,671 written for an hour — $0.078443, what Copilot charged
	if got, want := p.CostSplit(2, 4, 0, 15671, 15671), (2*4.0+4*20+15671*5)/1e6; !near(got, want) {
		t.Errorf("copilot 1h write call = %v, want %v", got, want)
	}
}

// OneHourFor fills only a price of Anthropic's: the maker's, a Claude
// account's, or one no provider is named for, which falls back to the
// maker's. Another provider's, and a model that is not Claude's, stand.
func TestOneHourForProvider(t *testing.T) {
	base := Price{Input: 5, Output: 25, CacheRead: 0.5, CacheWrite: 6.25}
	for _, who := range []string{"", "anthropic", "claude"} {
		p := base
		OneHourFor(who, "claude-sonnet-5-5", &p)
		if p.CacheWrite1h != 10 {
			t.Errorf("OneHourFor(%q) 1h = %v, want 10", who, p.CacheWrite1h)
		}
	}
	for _, who := range []string{"copilot", "openrouter", "github-copilot"} {
		p := base
		OneHourFor(who, "claude-sonnet-5-5", &p)
		if p.CacheWrite1h != 0 {
			t.Errorf("OneHourFor(%q) 1h = %v, want none", who, p.CacheWrite1h)
		}
	}
	p := base
	OneHourFor("", "gpt-6-astra", &p)
	if p.CacheWrite1h != 0 {
		t.Errorf("gpt given a 1-hour write: %v", p.CacheWrite1h)
	}
	// a price that has a 1-hour part keeps it, whoever it is for
	p = base
	p.CacheWrite1h = 9
	OneHourFor("", "claude-sonnet-5-5", &p)
	if p.CacheWrite1h != 9 {
		t.Errorf("1h given = %v, want it kept", p.CacheWrite1h)
	}
	// a model with no 5-minute write to speak of stays without one
	p = base
	p.CacheWrite = 0
	OneHourFor("", "claude-sonnet-5-5", &p)
	if p.CacheWrite1h != 0 {
		t.Errorf("no 5-minute write: 1h = %v, want none", p.CacheWrite1h)
	}
}
