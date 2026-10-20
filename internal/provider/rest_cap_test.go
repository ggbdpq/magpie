package provider

import "testing"

// #3076 (binbinah): the rests a user sets on a provider the agent is
// signed in to (codex and friends) never reached the accounts the gateway
// routes with. withPicks lays the user's picks from providers.json over
// every signed-in account and copied the queue, the limits and the models,
// not the rests: a `magpie provider rest-cap codex 4h` was written to
// providers.json and read back as nothing, so every Codex account kept
// the gateway's own 10/30-minute ladder after a 529.
func TestWithPicksKeepsTheRest(t *testing.T) {
	pk := Provider{ID: "codex", QuotaRest: 90, QuotaResetAt: "08:00", RestCap: 240}
	a := withPicks(Provider{ID: "codex"}, pk)
	if a.QuotaRest != 90 || a.QuotaResetAt != "08:00" || a.RestCap != 240 {
		t.Fatalf("an account of the provider keeps its rests: quota rest %d, reset at %q, rest cap %d, want 90, \"08:00\", 240", a.QuotaRest, a.QuotaResetAt, a.RestCap)
	}
	// off is off: no rest at all after a failure that says nothing of why,
	// not the gateway's own ladder
	a = withPicks(Provider{ID: "codex"}, Provider{ID: "codex", RestCap: RestCapOff})
	if a.RestCap != RestCapOff || !a.NoFailRest() {
		t.Fatalf("an account of a provider set off: rest cap %d (NoFailRest %v), want %d", a.RestCap, a.NoFailRest(), RestCapOff)
	}
	// and nothing set carries nothing: the picks stand over what the
	// account had, as they do over the rest of the overlay
	a = withPicks(Provider{ID: "codex", RestCap: 5, QuotaRest: 15}, Provider{ID: "codex"})
	if a.RestCap != 0 || a.QuotaRest != 0 || a.QuotaResetAt != "" {
		t.Fatalf("an account of a provider with no rests set: %d, %d, %q, want the gateway's own zeroes", a.RestCap, a.QuotaRest, a.QuotaResetAt)
	}
}

// The way the reporter saw it: the cap is written (`SetRestCap`, as
// `magpie provider rest-cap` does) and read back (`Find`, as the command's
// echo and GET /api/providers both do) on a signed-in account.
func TestRestCapReachesTheAccount(t *testing.T) {
	signIn(t)
	rememberLogins(true)
	if _, err := Find("codex"); err != nil {
		t.Fatalf("codex signed in: %v", err)
	}
	if err := SetRestCap("codex", 240); err != nil {
		t.Fatal(err)
	}
	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if p.RestCap != 240 {
		t.Fatalf("a rest cap set on codex reads back %d, want 240", p.RestCap)
	}
	if err := SetRestCap("codex", RestCapOff); err != nil {
		t.Fatal(err)
	}
	if p, err = Find("codex"); err != nil || !p.NoFailRest() {
		t.Fatalf("rest-cap off reads back %+v %v, want NoFailRest", p, err)
	}
	if err := SetRestCap("codex", 0); err != nil {
		t.Fatal(err)
	}
	if p, err = Find("codex"); err != nil || p.RestCap != 0 || p.NoFailRest() {
		t.Fatalf("the gateway's own reads back %+v %v, want rest cap 0", p, err)
	}
}
