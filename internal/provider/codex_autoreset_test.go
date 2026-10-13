package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// Only the week's window being used up counts: the five hours pass on
// their own, and a window whose end went by says nothing.
func TestWeekUsedUp(t *testing.T) {
	now := time.Now()
	later, gone := now.Add(48*time.Hour), now.Add(-time.Minute)
	five := QuotaWindow{Span: 5 * time.Hour, Used: 100, ResetsAt: &later}
	week := func(used float64, at *time.Time) QuotaWindow {
		return QuotaWindow{Span: 7 * 24 * time.Hour, Used: used, ResetsAt: at}
	}
	for name, c := range map[string]struct {
		ws   []QuotaWindow
		want bool
	}{
		"week used up":        {[]QuotaWindow{five, week(100, &later)}, true},
		"five hours only":     {[]QuotaWindow{five, week(99.5, &later)}, false},
		"week ended":          {[]QuotaWindow{week(100, &gone)}, false},
		"week end not known":  {[]QuotaWindow{week(100, nil)}, false},
		"on-demand, not week": {[]QuotaWindow{{Span: 30 * 24 * time.Hour, Used: 100, ResetsAt: &later, Aside: true}}, false},
	} {
		if got := weekUsedUp(c.ws, now) != nil; got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

// The setting is kept by account, whatever its case, and turned off again.
func TestSetCodexAutoReset(t *testing.T) {
	signIn(t)
	if err := SetCodexAutoReset(" Me@Example.com ", true); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load().CodexAutoReset; !slices.Equal(got, []string{"me@example.com"}) {
		t.Fatalf("kept %v", got)
	}
	if !CodexAutoReset("ME@example.com") || CodexAutoReset("other@example.com") || CodexAutoReset("") {
		t.Fatal("read back wrong")
	}
	if err := SetCodexAutoReset("me@example.com", false); err != nil {
		t.Fatal(err)
	}
	if CodexAutoReset("me@example.com") || len(settings.Load().CodexAutoReset) != 0 {
		t.Fatal("still on")
	}
}

// A reset spent by itself is kept past a restart: none is spent again
// until the week it was spent in ends.
func TestAutoResetKeptPastRestart(t *testing.T) {
	signIn(t)
	if err := SetCodexAutoReset("me@example.com", true); err != nil {
		t.Fatal(err)
	}
	a := &autoReset
	a.Lock()
	a.path = "" // read afresh
	a.load()
	a.spent["me@example.com"] = autoResetSpent{Until: time.Now().Add(72 * time.Hour), At: time.Now().Add(-time.Hour), Out: ResetOutcome{Code: "reset", Windows: 2}}
	a.save()
	a.path = "" // as a new magpie would
	a.Unlock()
	fakeCodexResets(t, 1, nil) // would say the week is not used up, if asked
	out, err := AutoUseCodexReset(t.Context(), "me@example.com")
	if err != nil || out.Code != "" {
		t.Fatalf("%+v %v", out, err)
	}
	a.Lock()
	_, kept := a.spent["me@example.com"]
	a.Unlock()
	if !kept {
		t.Fatal("what was spent was forgotten")
	}
}

// A codex-autoreset.json a crash left unreadable (#1505's zeroed file, or
// a half-written one) is copied aside before a use is written over it, as
// codex-resets-used.json's is: read as none it would have the week's
// auto-use spend a second reset, and the write would take the evidence
// with it. The use itself is still kept.
func TestAutoResetUnreadableKeptAside(t *testing.T) {
	signIn(t)
	if err := SetCodexAutoReset("me@example.com", true); err != nil {
		t.Fatal(err)
	}
	p := autoReset.where()
	os.MkdirAll(filepath.Dir(p), 0o755)
	bad := strings.Repeat("\x00", 64) // what a hard power-off left of #1505's files
	if err := os.WriteFile(p, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &autoReset
	a.Lock()
	a.path = "" // read afresh, as a new magpie would
	a.Unlock()

	soon := time.Now().Add(20 * time.Minute).UTC().Format(time.RFC3339)
	var consumed atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/wham/usage":
			json.NewEncoder(w).Encode(map[string]any{"plan_type": "pro",
				"rate_limit": map[string]any{
					"primary_window":   map[string]any{"used_percent": 10, "limit_window_seconds": 18000},
					"secondary_window": map[string]any{"used_percent": 100, "limit_window_seconds": 604800, "reset_at": time.Now().Add(72 * time.Hour).Unix()},
				},
				"rate_limit_reset_credits": map[string]any{"available_count": 2}})
		case "/backend-api/wham/rate-limit-reset-credits":
			json.NewEncoder(w).Encode(map[string]any{"available_count": 2, "credits": []any{
				map[string]any{"id": "soon", "status": "available", "expires_at": soon},
			}})
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			consumed.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"code": "reset", "windows_reset": 2})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(fake.Close)
	oldBase := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = oldBase })

	if out, err := AutoUseCodexReset(t.Context(), "me@example.com"); err != nil || out.Code != "reset" {
		t.Fatalf("the week used up: %+v %v", out, err)
	}
	aside, _ := filepath.Glob(p + ".bad-*")
	if len(aside) != 1 {
		t.Fatalf("the unreadable file was written over with nothing kept aside: %v", aside)
	}
	if kept, _ := os.ReadFile(aside[0]); string(kept) != bad {
		t.Fatalf("the copy holds %q, want the unreadable bytes", kept)
	}
	// and the use itself is kept: read afresh, no second one this week
	a.Lock()
	a.path = "" // as a new magpie would
	a.load()
	_, ok := a.spent["me@example.com"]
	a.Unlock()
	if !ok {
		t.Fatal("what was spent was forgotten")
	}
	fakeCodexResets(t, 2, nil) // would say the week is not used up, if asked
	if out, err := AutoUseCodexReset(t.Context(), "me@example.com"); err != nil || out.Code != "" {
		t.Fatalf("a second one the same week: %+v %v", out, err)
	}
	if consumed.Load() != 1 {
		t.Fatalf("consumed %d resets, want 1", consumed.Load())
	}
}
