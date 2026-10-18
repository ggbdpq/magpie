package provider

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

// The two times an account spends its reset at are set one by one, and
// SetCodexAutoResetCase read the other time before the update's lock: two
// calls that each turned one time on for the same account, at once, lost
// one of them — each save was built from the flags as they read before
// either call (#2813). Round after round, both must be kept.
func TestSetCodexAutoResetCaseConcurrentKeepsBothCases(t *testing.T) {
	signIn(t)
	const rounds = 100
	lost := 0
	for i := range rounds {
		u := fmt.Sprintf("both%d@example.com", i)
		start := make(chan struct{})
		var wg sync.WaitGroup
		var errs []error
		var mu sync.Mutex
		for _, when := range []string{ResetCaseWeek, ResetCaseExpiring} {
			wg.Go(func() {
				<-start
				if err := SetCodexAutoResetCase(u, when, true); err != nil {
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
		if week, expiring := CodexAutoResetWhen(u); !week || !expiring {
			lost++
		}
	}
	if lost != 0 {
		t.Fatalf("%d of %d rounds lost a case", lost, rounds)
	}
}
