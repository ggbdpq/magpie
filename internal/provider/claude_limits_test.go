package provider

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// A usage-based enterprise seat has no 5-hour or 7-day window: its /usage
// prints only the subscription notice, and the one reading it has is the
// overage window Claude Code told as it answered. That reading is kept
// instead of "temporarily unavailable" (#4241).
func TestClaudeOverageSeatKeepsItsWindow(t *testing.T) {
	var out atomic.Value
	out.Store("You are currently using your subscription to power your Claude Code usage\n")
	fakeClaudeUsage(t, &out, nil)
	reset := time.Now().Add(12 * time.Hour).Truncate(time.Second)
	NoteClaudeLimits("seat@example.com", []ClaudeLimit{{Kind: "overage", Used: 0.84, ResetsAt: reset.Unix()}})
	AskClaudeUsage()
	ws, err := claudeWindows(context.Background(), "seat@example.com", true)
	if err != nil || len(ws) != 1 || ws[0].Name != "Extra usage" || ws[0].Used != 84 || ws[0].ResetsAt == nil || !ws[0].ResetsAt.Equal(reset) {
		t.Fatalf("seat: %v %+v", err, ws)
	}
}

// What Claude Code says as it answers is the account's allowance, without
// its /usage, which keeps the windows it alone tells.
func TestNoteClaudeLimits(t *testing.T) {
	var out atomic.Value
	out.Store("")
	var fail atomic.Bool
	fail.Store(true) // /usage fails
	fakeClaudeUsage(t, &out, &fail)
	reset := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	claudeUsage.Lock()
	claudeUsage.m = map[string]claudeUsageEntry{"kev@example.com": {at: time.Now().Add(-time.Hour), ws: []QuotaWindow{
		{Name: "7 days · Opus", Used: 30, Model: "opus"}, {Name: "5 hours", Used: 5},
	}}}
	claudeUsage.Unlock()
	defer func() { claudeUsage.Lock(); claudeUsage.m = nil; claudeUsage.Unlock() }()

	NoteClaudeLimits("Kev@example.com", []ClaudeLimit{{Kind: "five_hour", Used: 0.42, ResetsAt: reset.Unix()}, {Kind: "seven_day", Used: 0.1}, {Kind: "overage", Used: 1}})
	ws, err := claudeWindows(context.Background(), "kev@example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, w := range ws {
		names = append(names, w.Name)
	}
	if len(ws) != 4 || ws[0].Name != "5 hours" || ws[0].Used != 42 || !ws[0].ResetsAt.Equal(reset) || ws[0].Span != 5*time.Hour || ws[1].Name != "7 days" || ws[1].Used != 10 || ws[2].Name != "7 days · Opus" || ws[3].Name != "Extra usage" || ws[3].Used != 100 || ws[3].Span != 0 {
		t.Fatalf("windows: %v %+v", names, ws)
	}

	// asked, /usage failing leaves what was heard
	AskClaudeUsage()
	claudeUsage.Lock()
	e := claudeUsage.m["kev@example.com"]
	e.at = time.Now().Add(-10 * time.Minute)
	e.tried = time.Now().Add(-time.Hour) // past the floor
	claudeUsage.m["kev@example.com"] = e
	claudeUsage.Unlock()
	if ws, err := claudeWindows(context.Background(), "kev@example.com", true); err != nil || len(ws) != 4 {
		t.Fatalf("after the endpoint failed: %v %v", ws, err)
	}
	// heard long ago, the failure is told
	AskClaudeUsage()
	claudeUsage.Lock()
	e.heard = time.Now().Add(-2 * time.Hour)
	e.tried = time.Now().Add(-time.Hour)
	claudeUsage.m["kev@example.com"] = e
	claudeUsage.Unlock()
	if _, err := claudeWindows(context.Background(), "kev@example.com", true); err == nil {
		t.Fatal("an old word hid the endpoint's failure")
	}
}
