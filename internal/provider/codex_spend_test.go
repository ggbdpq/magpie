package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// A ChatGPT workspace billed by usage (plan_type
// self_serve_business_usage_based) has no allowance at all (#4259): its
// /wham/usage answers no rate-limit windows and puts the account's
// allowance under spend_control instead — an individual limit in credits
// (186.58 of 5100, reset with the billing period), which only its reached
// was read of. The window is the codexSpendWindow read of it: the seat's
// one window, no credits to go on past (the cap is what the credits are
// spent against), and what the cap doesn't say is no window at all —
// unknown, never 0 or full — while a plan with windows of its own keeps
// exactly those.
func TestCodexSpendControlWindow(t *testing.T) {
	read := func(name string) map[string]any {
		t.Helper()
		b, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	with := func(m map[string]any, edit func(m map[string]any)) map[string]any {
		edit(m)
		return m
	}
	limit := func(m map[string]any) map[string]any {
		return m["spend_control"].(map[string]any)["individual_limit"].(map[string]any)
	}
	reset := time.Unix(1793491200, 0)
	for _, c := range []struct {
		name string
		body map[string]any
		want []QuotaWindow
	}{
		{"the spend control's one window, share, count and reset", read("codex_usage_workspace_spend.json"),
			[]QuotaWindow{{Name: "Credits", Used: 4, Amount: 186.58448004722595, Limit: 5100, Unit: "credit", ResetsAt: &reset}}},
		{"the share reckoned from the count, used_percent gone", with(read("codex_usage_workspace_spend.json"), func(m map[string]any) {
			delete(limit(m), "used_percent")
		}),
			[]QuotaWindow{{Name: "Credits", Used: 186.58448004722595 / 5100 * 100, Amount: 186.58448004722595, Limit: 5100, Unit: "credit", ResetsAt: &reset}}},
		{"figures as numbers, not strings", with(read("codex_usage_workspace_spend.json"), func(m map[string]any) {
			limit(m)["limit"], limit(m)["used"] = 5100, 186.58448004722595
		}),
			[]QuotaWindow{{Name: "Credits", Used: 4, Amount: 186.58448004722595, Limit: 5100, Unit: "credit", ResetsAt: &reset}}},
		{"a plan with windows of its own keeps them", read("codex_usage_team_spend_cap.json"),
			[]QuotaWindow{{Name: "5 hours", Used: 100}, {Name: "7 days", Used: 63}}},
		{"no individual limit reads as no window", with(read("codex_usage_workspace_spend.json"), func(m map[string]any) {
			m["spend_control"].(map[string]any)["individual_limit"] = nil
		}), []QuotaWindow{}},
		{"no spend control at all reads as no window", with(read("codex_usage_workspace_spend.json"), func(m map[string]any) {
			m["spend_control"] = nil
		}), []QuotaWindow{}},
		{"a cap of nothing reads as no window", with(read("codex_usage_workspace_spend.json"), func(m map[string]any) {
			limit(m)["limit"] = "0"
		}), []QuotaWindow{}},
		{"an unreadable limit reads as no window", with(read("codex_usage_workspace_spend.json"), func(m map[string]any) {
			limit(m)["limit"] = "lots"
		}), []QuotaWindow{}},
		{"no share to read reads as no window", with(read("codex_usage_workspace_spend.json"), func(m map[string]any) {
			delete(limit(m), "used_percent")
			delete(limit(m), "used")
		}), []QuotaWindow{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(c.body)
			}))
			defer fake.Close()
			old := CodexBase
			CodexBase = fake.URL + "/backend-api/codex"
			defer func() { CodexBase = old }()
			plan, windows, _, _, _, err := codexWindows(context.Background(), "tok", "acct-1")
			if err != nil {
				t.Fatalf("err %v", err)
			}
			if plan != c.body["plan_type"] {
				t.Errorf("plan %q, want %q", plan, c.body["plan_type"])
			}
			if len(windows) != len(c.want) {
				t.Fatalf("windows %v, want %v", windows, c.want)
			}
			for i, w := range c.want {
				got := windows[i]
				if got.Name != w.Name || got.Used != w.Used || got.Amount != w.Amount || got.Limit != w.Limit || got.Unit != w.Unit {
					t.Errorf("window %d = %+v, want %+v", i, got, w)
				}
				// what only the spend window is read as: no credits to go on
				// past (the cap is what the credits are spent against), and
				// reset when the billing period does
				if w.Name != "Credits" {
					continue
				}
				if got.Credits {
					t.Errorf("window %d reads as credits to go on past, want none: the cap is what the credits are spent against", i)
				}
				if w.ResetsAt == nil || got.ResetsAt == nil || !got.ResetsAt.Equal(*w.ResetsAt) {
					t.Errorf("window %d resets at %v, want %v", i, got.ResetsAt, w.ResetsAt)
				}
			}
		})
	}
}
