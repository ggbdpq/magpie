package provider

import (
	"testing"
	"time"
)

// A part of an account's points expiring before its real reset (RunsOut
// with RunsOutLeft told, less than what is left) is not the window ending
// then: Renewal keeps the window at its reset, so Smart doesn't rank the
// account first as one whose whole left is lost at the part's expiry and
// send it every request before a peer whose reset is the sooner (#5791).
// What the vendor doesn't count (RunsOutLeft or Of untold), a part that
// covers all the window has left, and an account with no reset of its own
// (WorkBuddy's credits) still end the window at RunsOut.
func TestPartialExpiryKeepsTheReset(t *testing.T) {
	now := time.Now()
	day := 24 * time.Hour
	hour := time.Hour

	// the issue's A: 500 credits, 1% used (495 left), one credit lapsing in
	// 14 hours, the window itself renewing in 28 days
	a := Allowance{{Used: 1, Resets: now.Add(28 * day), Span: 28 * day,
		RunsOut: now.Add(14 * hour), RunsOutLeft: 1, Of: 500, Unit: "credits"}}
	if r := a.Renewal("m", now); len(r) != 1 || !r[0].Equal(now.Add(28*day)) {
		t.Fatalf("a part expiring renews at its reset: %v, want %v", r, now.Add(28*day))
	}

	// what the vendor doesn't count is lost whole, as before: the window
	// ends at RunsOut
	uncounted := Allowance{{Used: 1, Resets: now.Add(28 * day), Span: 28 * day,
		RunsOut: now.Add(14 * hour)}}
	if r := uncounted.Renewal("m", now); len(r) != 1 || !r[0].Equal(now.Add(14*hour)) {
		t.Fatalf("a lapse of untold size renews at RunsOut: %v, want %v", r, now.Add(14*hour))
	}
	// a part covering all that is left ends the window too
	allLeft := Allowance{{Used: 99, Resets: now.Add(28 * day), Span: 28 * day,
		RunsOut: now.Add(14 * hour), RunsOutLeft: 495, Of: 500, Unit: "credits"}}
	if r := allLeft.Renewal("m", now); len(r) != 1 || !r[0].Equal(now.Add(14*hour)) {
		t.Fatalf("a lapse of all that is left renews at RunsOut: %v, want %v", r, now.Add(14*hour))
	}
	// an account with no reset of its own (WorkBuddy's credits) goes by the
	// package that ends first
	noReset := Allowance{{Used: 10, RunsOut: now.Add(3 * day)}}
	if r := noReset.Renewal("m", now); len(r) != 1 || !r[0].Equal(now.Add(3*day)) {
		t.Fatalf("no reset, renewal at RunsOut: %v, want %v", r, now.Add(3*day))
	}
}
