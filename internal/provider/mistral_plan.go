package provider

// Mistral tells a key its month's budgets at api.mistral.ai/v1/billing/
// budget (#5087): what the plan's included API usage has spent of its
// own total, and what its separate Vibe Code usage has of another, with
// the moment both come back (admin.mistral.ai/subscription shows the
// same). The key goes in x-api-key, the dashboard's own header, not in
// Authorization as the inference API takes it. The plan's own query,
// /v1/billing/subscription, names the plan and tells whether
// pay-as-you-go is on: a budget spent past then draws on the
// prepayment, so using it up doesn't stop the key.

import (
	"context"
	"encoding/json"
	"time"
)

// mistralBudgetURL is the budgets query, a var for tests to point at a
// server of their own.
var mistralBudgetURL = "https://api.mistral.ai/v1/billing/budget"

// mistralSubscriptionURL is the plan's own query, likewise.
var mistralSubscriptionURL = "https://api.mistral.ai/v1/billing/subscription"

// mistralBudget is one of the two budgets of a billing period
// (BudgetDetail in Mistral's OpenAPI spec): the share spent, the total,
// its currency, and when the month is up. The currency is left out when
// no budget could be derived for the period.
type mistralBudget struct {
	UsedPct  float64 `json:"usage_percentage"`
	Total    float64 `json:"initial_budget"`
	Currency string  `json:"currency"`
	ResetAt  string  `json:"reset_at"`
	Payg     bool    `json:"payg_enabled"`
}

// mistralBudgets is /v1/billing/budget. Either budget is left out of a
// reply that has none of it, and an org with no budget (a free or
// pay-as-you-go one) is told none at all.
type mistralBudgets struct {
	API  *mistralBudget `json:"api_budget"`
	Vibe *mistralBudget `json:"vibe_budget"`
}

// readMistralBudget is the budgets as windows: the plan's included API
// usage first, the Vibe Code usage beside it (Aside: Vibe running out
// stops no API request), each what the month has spent of its total and
// when it comes back. A reply with neither budget has no card.
func readMistralBudget(b []byte) (string, []QuotaWindow, error) {
	var r mistralBudgets
	if err := json.Unmarshal(b, &r); err != nil {
		return "", nil, err
	}
	out := []QuotaWindow{}
	if w := r.API.window("API", false); w != nil {
		out = append(out, *w)
	}
	if w := r.Vibe.window("Vibe Code", true); w != nil {
		out = append(out, *w)
	}
	return "", out, nil
}

// window is the budget as a window, nil for one the account hasn't: a
// budget with no total is none. One whose currency wasn't derived shows
// its share alone.
func (m *mistralBudget) window(name string, aside bool) *QuotaWindow {
	if m == nil || m.Total <= 0 {
		return nil
	}
	w := &QuotaWindow{Name: name, Used: min(100, max(0, m.UsedPct))}
	if m.Currency != "" {
		w.Amount = min(m.Total, max(0, m.Total*m.UsedPct/100))
		w.Limit = m.Total
		w.Unit = m.Currency
	}
	if t, err := time.Parse(time.RFC3339, m.ResetAt); err == nil {
		w.ResetsAt = &t
	}
	w.Aside = aside
	return w
}

// mistralKeyWindows is a Mistral key's card: the month's budgets, and,
// asked beside them, the plan's name and whether pay-as-you-go is on —
// the API budget spent past draws on the prepayment then, so using it
// up doesn't stop the key. The plan query failing leaves the budgets as
// they are: its name is a label, not the card. An org with no plan
// tells no budgets, and gets no card.
func mistralKeyWindows(ctx context.Context, src planQuotaSource, key string) (string, []QuotaWindow, error) {
	b, err := planBody(ctx, src, key)
	if err != nil {
		return "", nil, err
	}
	plan, ws, err := readMistralBudget(b)
	if err != nil || len(ws) == 0 {
		return plan, ws, err
	}
	if sb, serr := planBody(ctx, planQuotaSource{mistralSubscriptionURL, false, true, nil, false}, key); serr == nil {
		var s struct {
			Plan string `json:"plan_name"`
			Payg bool   `json:"payg_enabled"`
		}
		if json.Unmarshal(sb, &s) == nil {
			plan = s.Plan
			for i := range ws {
				if ws[i].Name != "Vibe Code" {
					ws[i].Aside = s.Payg
				}
			}
		}
	}
	return plan, ws, nil
}
