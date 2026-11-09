package provider

import (
	"context"
	"testing"
	"time"
)

// A quota reading must not keep the plan from sign-in over the one Code
// Assist gives now. A paid Antigravity account can still be on free-tier:
// its paidTier, not currentTier or the saved plan, names its membership.
func TestGoogleQuotaPlanFollowsCodeAssist(t *testing.T) {
	const free = `{"currentTier":{"id":"free-tier","name":"Antigravity"},
		"paidTier":{"id":"free-tier","name":"Antigravity Starter Quota"},
		"cloudaicompanionProject":"aicode-consumers"}`
	const pro = `{"currentTier":{"id":"free-tier","name":"Antigravity"},
		"paidTier":{"id":"g1-pro-tier","name":"Google AI Pro"},
		"cloudaicompanionProject":"aicode-consumers"}`
	const ultra = `{"currentTier":{"id":"standard-tier","name":"Antigravity"},
		"paidTier":{"id":"g1-ultra-tier","name":"Google AI Ultra"},
		"cloudaicompanionProject":"aicode-consumers"}`
	for _, agent := range []string{"antigravity", "gemini"} {
		for _, c := range []struct {
			name, saved, load, want string
			failed                  bool
		}{
			{"upgraded to Pro", "Antigravity Starter Quota", pro, "Google AI Pro", false},
			{"upgraded to Ultra", "Antigravity Starter Quota", ultra, "Google AI Ultra", false},
			{"downgraded", "Google AI Pro", free, "Antigravity Starter Quota", false},
			{"no saved plan", "", pro, "Google AI Pro", false},
			{"no plan returned", "Google AI Pro", `{"currentTier":{},"cloudaicompanionProject":"aicode-consumers"}`, "Google AI Pro", false},
			{"failed load", "Google AI Pro", `not json`, "Google AI Pro", true},
		} {
			t.Run(agent+"/"+c.name, func(t *testing.T) {
				f := &fakeGoogle{load: c.load,
					models: `{"models":{"gemini-2.5-flash":{"quotaInfo":{"remainingFraction":0.5}}}}`,
					quota:  `{"buckets":[{"modelId":"gemini-2.5-flash","remainingFraction":0.5}]}`}
				googleSandbox(t, f)
				auth := googleAuth{AccessToken: "tok", RefreshToken: "rt-plan", Expiry: time.Now().Add(time.Hour).UnixMilli()}
				if err := addGoogleLogin(agent, "plan@example.com", c.saved, auth); err != nil {
					t.Fatal(err)
				}
				q := googleLoginQuota(context.Background(), googleLoginList(agent)[0])
				if (q.Error != "") != c.failed {
					t.Fatalf("quota error %q, want failed %v", q.Error, c.failed)
				}
				if q.Plan != c.want {
					t.Errorf("plan %q, want %q (saved %q)", q.Plan, c.want, c.saved)
				}
				// the accounts list, the card and Routing's rows read the
				// saved plan, not the quota's
				if l := Logins(agent); len(l) != 1 || l[0].Plan != c.want {
					t.Errorf("saved account after the reading: %+v, want plan %q", l, c.want)
				}
				if !c.failed && (len(q.Windows) != 1 || q.Windows[0].Model != "gemini-2.5-flash" || q.Windows[0].Used != 50) {
					t.Errorf("windows %+v, want gemini-2.5-flash at 50%% used", q.Windows)
				}
			})
		}
	}
}
