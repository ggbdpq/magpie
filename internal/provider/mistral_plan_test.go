package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mistralBudgetReply is /v1/billing/budget for a plan with both its
// budgets, in euros, as Mistral's OpenAPI spec gives BudgetDetail.
const mistralBudgetReply = `{
  "vibe_budget": {"usage_percentage": 42.5, "initial_budget": 300.0,
                  "currency": "EUR", "reset_at": "2026-12-01T00:00:00Z", "payg_enabled": false},
  "api_budget":  {"usage_percentage": 10, "initial_budget": 25.0,
                  "currency": "EUR", "reset_at": "2026-12-01T00:00:00Z", "payg_enabled": false}
}`

// The plan's included API usage is the first window, its Vibe Code usage
// the second, apart from it (Aside: Vibe running out stops no API
// request), each what the month has spent of its own total and when the
// month is up.
func TestReadMistralBudget(t *testing.T) {
	plan, ws, err := readMistralBudget([]byte(mistralBudgetReply))
	if err != nil || plan != "" || len(ws) != 2 {
		t.Fatalf("%v %q %+v", err, plan, ws)
	}
	reset := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	w := ws[0]
	if w.Name != "API" || w.Amount != 2.5 || w.Limit != 25 || w.Unit != "EUR" || w.Used != 10 ||
		w.ResetsAt == nil || !w.ResetsAt.Equal(reset) || w.Aside {
		t.Errorf("api: %+v", w)
	}
	v := ws[1]
	if v.Name != "Vibe Code" || v.Amount != 127.5 || v.Limit != 300 || v.Unit != "EUR" || v.Used != 42.5 || !v.Aside {
		t.Errorf("vibe: %+v", v)
	}
}

// What a reply leaves out or has none of is no window, and an org with
// no budget (a free or pay-as-you-go one) has no card.
func TestReadMistralBudgetMissing(t *testing.T) {
	_, ws, err := readMistralBudget([]byte(`{"vibe_budget": null,
		"api_budget": {"usage_percentage": 3, "initial_budget": 20, "currency": "USD", "reset_at": "2026-12-01T00:00:00Z"}}`))
	if err != nil || len(ws) != 1 || ws[0].Name != "API" || ws[0].Unit != "USD" {
		t.Fatalf("%v %+v", err, ws)
	}
	_, ws, err = readMistralBudget([]byte(`{"api_budget": {"usage_percentage": 0, "initial_budget": 0,
		"currency": null, "reset_at": "2026-12-01T00:00:00Z"}, "vibe_budget": null}`))
	if err != nil || len(ws) != 0 {
		t.Fatalf("no budget: %v %+v", err, ws)
	}
	// a budget with no currency derived shows its share alone
	_, ws, err = readMistralBudget([]byte(`{"api_budget": {"usage_percentage": 30, "initial_budget": 10,
		"currency": null, "reset_at": "2026-12-01T00:00:00Z"}}`))
	if err != nil || len(ws) != 1 || ws[0].Unit != "" || ws[0].Amount != 0 || ws[0].Limit != 0 || ws[0].Used != 30 {
		t.Fatalf("no currency: %v %+v", err, ws)
	}
}

// Both queries take the key in x-api-key alone, the plan's name comes
// with the budgets, and pay-as-you-go on leaves a spent API budget
// going (it draws on the prepayment), while the Vibe Code budget never
// stops an API request.
func TestMistralKeyWindows(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path+" x-api-key:"+r.Header.Get("x-api-key")+" auth:"+r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/v1/billing/budget":
			w.Write([]byte(mistralBudgetReply))
		case "/v1/billing/subscription":
			w.Write([]byte(`{"plan_name":"INDIVIDUAL","payg_enabled":true,"vibe_payg_enabled":false,"enterprise":false}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	budget, subscription := mistralBudgetURL, mistralSubscriptionURL
	mistralBudgetURL, mistralSubscriptionURL = srv.URL+"/v1/billing/budget", srv.URL+"/v1/billing/subscription"
	defer func() { mistralBudgetURL, mistralSubscriptionURL = budget, subscription }()
	src := planQuotaSource{mistralBudgetURL, false, true, readMistralBudget, false}
	plan, ws, err := mistralKeyWindows(context.Background(), src, "k1")
	if err != nil || plan != "INDIVIDUAL" || len(ws) != 2 {
		t.Fatalf("%v %q %+v", err, plan, ws)
	}
	if !ws[0].Aside {
		t.Error("pay-as-you-go on: a spent API budget stops the key")
	}
	if !ws[1].Aside {
		t.Error("the Vibe Code budget out stops an API request")
	}
	if len(seen) != 2 {
		t.Fatalf("asks: %q", seen)
	}
	for _, s := range seen {
		if !strings.Contains(s, "x-api-key:k1") || !strings.HasSuffix(s, "auth:") {
			t.Errorf("ask: %s", s)
		}
	}
}

// The plan's own query failing leaves the budgets as they are: its name
// is a label, not the card.
func TestMistralKeyWindowsNoSubscription(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/billing/subscription" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte(mistralBudgetReply))
	}))
	defer srv.Close()
	budget, subscription := mistralBudgetURL, mistralSubscriptionURL
	mistralBudgetURL, mistralSubscriptionURL = srv.URL+"/v1/billing/budget", srv.URL+"/v1/billing/subscription"
	defer func() { mistralBudgetURL, mistralSubscriptionURL = budget, subscription }()
	src := planQuotaSource{mistralBudgetURL, false, true, readMistralBudget, false}
	plan, ws, err := mistralKeyWindows(context.Background(), src, "k1")
	if err != nil || plan != "" || len(ws) != 2 || ws[0].Aside {
		t.Errorf("%v %q %+v api aside %v", err, plan, ws, len(ws) > 0 && ws[0].Aside)
	}
}
