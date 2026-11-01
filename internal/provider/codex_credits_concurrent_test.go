package provider

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// Two setters that run at once each saved their own snapshot: Load and Save
// take settings' file lock on their own, so the second write dropped the
// first one's change (#2813). Round after round, each of two accounts turned
// off together must still be kept.
func TestSetCodexCreditsConcurrentKeepsBothAccounts(t *testing.T) {
	signIn(t)
	const rounds = 100
	lost := 0
	for i := range rounds {
		a, b := fmt.Sprintf("a%d@example.com", i), fmt.Sprintf("b%d@example.com", i)
		start := make(chan struct{})
		var wg sync.WaitGroup
		var errs []error
		var mu sync.Mutex
		for _, u := range []string{a, b} {
			wg.Go(func() {
				<-start
				if err := SetCodexCredits(u, false); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
			})
		}
		close(start)
		wg.Wait()
		if len(errs) != 0 {
			t.Fatal(errors.Join(errs...))
		}
		got := settings.Load().CodexNoCredits
		if !slices.Contains(got, a) || !slices.Contains(got, b) {
			lost++
		}
	}
	if lost != 0 {
		t.Fatalf("%d of %d rounds lost an update", lost, rounds)
	}
}

// Off and last live in two lists, and SetCodexCreditsMode built each save
// from its own snapshot of both: two accounts set at once, each to its own
// mode, lost one of them — the second write carried neither (#2813). Round
// after round, each list keeps its own account and only it.
func TestSetCodexCreditsModeConcurrentKeepsBothModes(t *testing.T) {
	signIn(t)
	const rounds = 100
	lost := 0
	for i := range rounds {
		a, b := fmt.Sprintf("last%d@example.com", i), fmt.Sprintf("off%d@example.com", i)
		start := make(chan struct{})
		var wg sync.WaitGroup
		var errs []error
		var mu sync.Mutex
		for _, m := range []struct{ user, mode string }{{a, CreditsLast}, {b, CreditsOff}} {
			wg.Go(func() {
				<-start
				if err := SetCodexCreditsMode(m.user, m.mode); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
			})
		}
		close(start)
		wg.Wait()
		if len(errs) != 0 {
			t.Fatal(errors.Join(errs...))
		}
		s := settings.Load()
		if !slices.Contains(s.CodexCreditsLast, a) || slices.Contains(s.CodexNoCredits, a) ||
			!slices.Contains(s.CodexNoCredits, b) || slices.Contains(s.CodexCreditsLast, b) {
			lost++
		}
	}
	if lost != 0 {
		t.Fatalf("%d of %d rounds lost a mode", lost, rounds)
	}
}
