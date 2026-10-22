package gateway

import (
	"net/http"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Concurrent requests on one provider can fail differently: a 402 out of
// credit rests the account for half an hour, and a 429 answering at the
// same moment — the first request still holding the vendor's only slot —
// rests it for the minute a rate limit starts at. Written last, the
// shorter rest used to replace the longer one (#3289, wjq990112), and the
// account was asked again as soon as it ended, refused, and rested again,
// round after round. A rest that ends later stands, as one narrowed to a
// model keeps the longer of the two; a longer rest still replaces a
// shorter one.
func TestShorterRestDoesNotReplaceLongerOne(t *testing.T) {
	forgetRouting()
	old := allowances
	allowances = func(string) map[string]provider.Allowance {
		return map[string]provider.Allowance{
			"me@x.com": {{Used: 5, Span: 30 * 24 * time.Hour}},
		}
	}
	oldStale := staleAllowance
	staleAllowance = func(string, string) {}
	defer func() { allowances, staleAllowance = old, oldStale }()

	acct := candidate{
		p:     provider.Provider{ID: "sub2tok", Account: &provider.Account{Agent: "factory", User: "me@x.com"}},
		model: "glm-5.3-flash",
		rest:  "sub2tok#me@x.com",
	}
	s := &Server{}
	s.Unrest(acct.restKey())

	credit := s.restAfter(acct, http.StatusPaymentRequired, http.Header{}, []byte(`{"error":{"message":"insufficient_balance"}}`))
	if credit.Why != failCredit || credit.By != "credit" {
		t.Fatalf("a 402 insufficient_balance rests by %q (%s), not credit", credit.By, credit.Why)
	}

	h := http.Header{}
	h.Set("Retry-After", "1")
	rate := s.restAfter(acct, http.StatusTooManyRequests, h, []byte(`{"error":{"message":"Rate limit exceeded","type":"rate_limit_error"}}`))
	if rate.Why != failRate {
		t.Fatalf("the 429 rests as %s, not rate", rate.Why)
	}

	r, ok := restOf(acct.restKey())
	if !ok {
		t.Fatal("the account rests no more")
	}
	if r.Why != failCredit {
		t.Fatalf("the rate rest replaced the credit rest: resting by %q (%s)", r.By, r.Why)
	}
	if d := time.Until(r.Until); d < creditRest-time.Minute {
		t.Fatalf("resting %v, not the credit rest of about %v", d.Round(time.Second), creditRest)
	}

	// a longer rest still replaces a shorter one
	quota := s.restAfter(acct, http.StatusTooManyRequests, http.Header{}, []byte(`{"error":{"type":"usage_limit_reached","resets_in_seconds":691200}}`))
	if quota.Why != failQuota || quota.By != "resets" {
		t.Fatalf("the 8-day refusal rests by %q (%s), not resets", quota.By, quota.Why)
	}
	if r, ok := restOf(acct.restKey()); !ok || r.Why != failQuota || time.Until(r.Until) < 7*24*time.Hour {
		t.Fatalf("the longer quota rest didn't take: %v (%s), %v", ok, r.Why, time.Until(r.Until).Round(time.Minute))
	}
}
