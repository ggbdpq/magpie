package usageapi

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/usage"
)

// PeriodOf takes a period a ?period= names, and refuses what no period
// parses to (#5985): no value at all keeps the default month, a known
// one is itself, and anything else — including reference.md's week and
// month, which the HTTP layer never took — is an error naming the shapes
// that work, never another period's data.
func TestPeriodOfKnownOrError(t *testing.T) {
	for _, in := range []struct {
		s    string
		want usage.Period
	}{
		{"", usage.Month},
		{"today", usage.Today},
		{"7d", usage.Week},
		{"30d", usage.Month},
		{"all", usage.All},
		{"last:24h", "last:24h"},
		{"2026-11-08..2026-11-09", "2026-11-08..2026-11-09"},
	} {
		p, err := PeriodOf(in.s)
		if err != nil {
			t.Fatalf("PeriodOf(%q): %v", in.s, err)
		}
		if p != in.want {
			t.Fatalf("PeriodOf(%q) = %q, want %q", in.s, p, in.want)
		}
	}
	for _, s := range []string{"week", "month", "1d", "24h", "bogus", "2026-11-08..2026-11-09..2026-11-10", "last:0"} {
		p, err := PeriodOf(s)
		if err == nil {
			t.Fatalf("PeriodOf(%q) = %q with no error, want one", s, p)
		}
		if !strings.Contains(err.Error(), "today|7d|30d|all") {
			t.Fatalf("PeriodOf(%q)'s error names no working shape: %v", s, err)
		}
	}
}
