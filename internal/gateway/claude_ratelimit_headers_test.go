package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// A reply a Claude subscription account answered says that account's
// allowance as Anthropic says it to Claude Code, in its
// anthropic-ratelimit-unified-* headers: a Claude Code signed in to
// claude.ai reads them into its status line's rate_limits, where
// claude-hud shows the 5-hour and weekly figures (#1257). The run's agent
// tells them in its rate_limit_event, which comes before the reply's first
// event, so the reply says this turn's.
func TestClaudeSubscriptionReplySaysTheAllowance(t *testing.T) {
	five := time.Now().Add(3 * time.Hour).Unix()
	week := time.Now().Add(4 * 24 * time.Hour).Unix()
	user := "hud-" + strconv.FormatInt(time.Now().UnixNano(), 36) + "@example.com"
	limits := `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","rateLimitType":"five_hour","utilization":0.42,"resetsAt":` + strconv.FormatInt(five, 10) +
		`,"unifiedWindows":{"five_hour":{"utilization":0.42,"resetsAt":` + strconv.FormatInt(five, 10) + `},"seven_day":{"utilization":0.17,"resetsAt":` + strconv.FormatInt(week, 10) + `}}}}`
	answer := []string{
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"m1","model":"claude-sonnet-5","usage":{"input_tokens":3,"output_tokens":1}}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"OK"}}}`,
		`{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}}`,
		`{"type":"stream_event","event":{"type":"message_stop"}}`,
	}
	ask := func(name, owner string, stream bool, lines []string) *httptest.ResponseRecorder {
		t.Helper()
		s := New()
		start := func(_ context.Context, req *Request) (*subscriptionRun, <-chan Event, error) {
			run := &subscriptionRun{bridge: s.subscription, token: "hud", pending: map[string]chan mcpToolResult{}}
			run.owner.Store(&owner)
			events := run.attach()
			go run.readOutput(strings.NewReader(strings.Join(lines, "\n") + "\n"))
			return run, events, nil
		}
		body := `{"model":"claude-sonnet-5","max_tokens":100,"stream":` + strconv.FormatBool(stream) + `,"messages":[{"role":"user","content":"hi"}]}`
		rec := httptest.NewRecorder()
		var u Usage
		if code, msg := s.serveSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, name, "claude-sonnet-5", owner, []byte(body), &u, start); code != 200 {
			t.Fatalf("%d %s", code, msg)
		}
		return rec
	}
	says := func(rec *httptest.ResponseRecorder, want map[string]string) {
		t.Helper()
		for k, v := range want {
			if got := rec.Header().Get(k); got != v {
				t.Errorf("%s: %q, want %q (headers %v)", k, got, v, rec.Header())
			}
		}
	}
	owner := "claude\x00" + user
	told := map[string]string{
		"anthropic-ratelimit-unified-5h-utilization": "0.42",
		"anthropic-ratelimit-unified-5h-reset":       strconv.FormatInt(five, 10),
		"anthropic-ratelimit-unified-7d-utilization": "0.17",
		"anthropic-ratelimit-unified-7d-reset":       strconv.FormatInt(week, 10),
	}
	for _, stream := range []bool{true, false} {
		t.Run("stream="+strconv.FormatBool(stream), func(t *testing.T) {
			// the turn tells it
			says(ask("Claude Code", owner, stream, append([]string{limits}, answer...)), told)
			// a turn that tells nothing says what is kept
			says(ask("Claude Code", owner, stream, answer), told)
		})
	}
	// another account's reply says nothing of this one's
	if rec := ask("Claude Code", "claude\x00other-"+user, true, answer); rec.Header().Get("anthropic-ratelimit-unified-5h-utilization") != "" {
		t.Errorf("another account's reply says %v", rec.Header())
	}
	// another agent's run (Codex's, Cursor's) has no Claude allowance to say
	if rec := ask("Agent", owner, true, answer); rec.Header().Get("anthropic-ratelimit-unified-5h-utilization") != "" {
		t.Errorf("another agent's reply says %v", rec.Header())
	}
}

// Through the gateway as it runs, to a Claude Code signed in to the
// account and answering as Claude Code does, streamed or not: the reply's
// headers carry what its rate_limit_event said, past the writer that holds
// a reply while another account may still take over.
func TestClaudeAllowanceHeadersThroughTheGateway(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), mustJSON(map[string]any{"claudeAiOauth": map[string]any{
		"accessToken": "tok-me", "refreshToken": "r-me", "expiresAt": time.Now().Add(24 * time.Hour).UnixMilli(), "subscriptionType": "pro"}}), 0o600)
	os.WriteFile(filepath.Join(home, ".claude.json"), mustJSON(map[string]any{
		"oauthAccount": map[string]any{"emailAddress": "hud@example.com", "accountUuid": "u-hud"}}), 0o600)
	five := strconv.FormatInt(time.Now().Add(2*time.Hour).Unix(), 10)
	week := strconv.FormatInt(time.Now().Add(5*24*time.Hour).Unix(), 10)
	dir := t.TempDir()
	testenv.Program(t, filepath.Join(dir, "claude"), `#!/bin/sh
if [ "$1" = auth ]; then echo '{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty","email":"hud@example.com","subscriptionType":"pro"}'; exit 0; fi
while read -r line; do
  echo '{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","isUsingOverage":false,"unifiedWindows":{"five_hour":{"utilization":0.63,"resetsAt":`+five+`},"seven_day":{"utilization":0.08,"resetsAt":`+week+`}}}}'
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-sonnet-5","usage":{"input_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pong"}}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":"pong"}'
done
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)
	s := New()
	t.Cleanup(s.subscription.abortAll)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	for _, stream := range []string{"true", "false"} {
		res, err := http.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(
			`{"model":"claude/claude-sonnet-5","max_tokens":50,"stream":`+stream+`,"messages":[{"role":"user","content":"ping `+stream+`"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 || !strings.Contains(string(b), "pong") {
			t.Fatalf("stream=%s: %d %s", stream, res.StatusCode, b)
		}
		for k, v := range map[string]string{
			"anthropic-ratelimit-unified-5h-utilization": "0.63",
			"anthropic-ratelimit-unified-5h-reset":       five,
			"anthropic-ratelimit-unified-7d-utilization": "0.08",
			"anthropic-ratelimit-unified-7d-reset":       week,
		} {
			if got := res.Header.Get(k); got != v {
				t.Errorf("stream=%s: %s %q, want %q (headers %v)", stream, k, got, v, res.Header)
			}
		}
	}
}

// A Claude subscription 429 for an exhausted allowance carries Anthropic's
// unified rejection headers (status, reset, representative-claim) so a
// downstream Claude Code signed in to claude.ai (including T3 Code) emits
// rate_limit_event with resetsAt and can auto-resume when the limit resets.
func TestClaudeSubscription429SaysUnifiedRejectionHeaders(t *testing.T) {
	five := time.Now().Add(2 * time.Hour).Unix()
	week := time.Now().Add(4 * 24 * time.Hour).Unix()
	user := "rej-" + strconv.FormatInt(time.Now().UnixNano(), 36) + "@example.com"
	owner := "claude\x00" + user
	rejectedEvent := `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"five_hour","resetsAt":` + strconv.FormatInt(five, 10) +
		`,"overageStatus":"rejected","unifiedWindows":{"five_hour":{"utilization":1,"resetsAt":` + strconv.FormatInt(five, 10) + `},"seven_day":{"utilization":0.25,"resetsAt":` + strconv.FormatInt(week, 10) + `}}}}`
	refusal := `{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"result":"You've hit your 5-hour limit · resets 1:20pm (Asia/Taipei)"}`

	ask := func(proto provider.Protocol, owner string, stream bool, lines []string) *httptest.ResponseRecorder {
		t.Helper()
		s := New()
		start := func(_ context.Context, req *Request) (*subscriptionRun, <-chan Event, error) {
			run := &subscriptionRun{bridge: s.subscription, token: "rej", pending: map[string]chan mcpToolResult{}}
			run.owner.Store(&owner)
			events := run.attach()
			go run.readOutput(strings.NewReader(strings.Join(lines, "\n") + "\n"))
			return run, events, nil
		}
		var body, url string
		if proto == provider.Anthropic {
			url = "/v1/messages"
			body = `{"model":"claude-sonnet-5","max_tokens":100,"stream":` + strconv.FormatBool(stream) + `,"messages":[{"role":"user","content":"hi"}]}`
		} else {
			url = "/v1/chat/completions"
			body = `{"model":"claude-sonnet-5","stream":` + strconv.FormatBool(stream) + `,"messages":[{"role":"user","content":"hi"}]}`
		}
		rec := httptest.NewRecorder()
		var u Usage
		if code, msg := s.serveSubscription(rec, httptest.NewRequest("POST", url, strings.NewReader(body)), proto, "Claude Code", "claude-sonnet-5", owner, []byte(body), &u, start); code != http.StatusTooManyRequests {
			t.Fatalf("code %d (%s), want 429", code, msg)
		}
		return rec
	}

	want := map[string]string{
		"anthropic-ratelimit-unified-status":               "rejected",
		"anthropic-ratelimit-unified-reset":                strconv.FormatInt(five, 10),
		"anthropic-ratelimit-unified-representative-claim": "five_hour",
		"anthropic-ratelimit-unified-overage-status":       "rejected",
		"anthropic-ratelimit-unified-5h-status":            "rejected",
		"anthropic-ratelimit-unified-5h-utilization":       "1",
		"anthropic-ratelimit-unified-5h-reset":             strconv.FormatInt(five, 10),
		"anthropic-ratelimit-unified-7d-utilization":       "0.25",
		"anthropic-ratelimit-unified-7d-reset":             strconv.FormatInt(week, 10),
	}
	for _, stream := range []bool{true, false} {
		t.Run("rate_limit_event/stream="+strconv.FormatBool(stream), func(t *testing.T) {
			rec := ask(provider.Anthropic, owner, stream, []string{rejectedEvent, refusal})
			for k, v := range want {
				if got := rec.Header().Get(k); got != v {
					t.Errorf("%s: %q, want %q (headers %v)", k, got, v, rec.Header())
				}
			}
		})
	}

	t.Run("non-anthropic client gets no unified rejection header", func(t *testing.T) {
		rec := ask(provider.Chat, owner, true, []string{rejectedEvent, refusal})
		if got := rec.Header().Get("anthropic-ratelimit-unified-status"); got != "" {
			t.Errorf("chat client got unified status %q", got)
		}
	})

	t.Run("plain rate-limit 429 without rejected event stays plain", func(t *testing.T) {
		freshOwner := "claude\x00plain-" + strconv.FormatInt(time.Now().UnixNano(), 36) + "@example.com"
		plain := `{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"result":"This request would exceed your account's rate limit. Please try again later."}`
		rec := ask(provider.Anthropic, freshOwner, true, []string{plain})
		if got := rec.Header().Get("anthropic-ratelimit-unified-status"); got != "" {
			t.Errorf("plain rate-limit got unified status %q (headers %v)", got, rec.Header())
		}
	})

	t.Run("refusal text fallback when no rate_limit_event arrived", func(t *testing.T) {
		taipei, err := time.LoadLocation("Asia/Taipei")
		if err != nil {
			t.Fatal(err)
		}
		target := time.Now().In(taipei).Add(90 * time.Minute).Truncate(time.Minute)
		clock := strings.ToLower(target.Format("3:04pm"))
		textOnly := `{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"result":"You've hit your 5-hour limit · resets ` + clock + ` (Asia/Taipei)"}`
		textOwner := "claude\x00text-" + strconv.FormatInt(time.Now().UnixNano(), 36) + "@example.com"
		rec := ask(provider.Anthropic, textOwner, true, []string{textOnly})
		if got := rec.Header().Get("anthropic-ratelimit-unified-status"); got != "rejected" {
			t.Fatalf("status %q, want rejected (headers %v)", got, rec.Header())
		}
		if got := rec.Header().Get("anthropic-ratelimit-unified-representative-claim"); got != "five_hour" {
			t.Fatalf("claim %q, want five_hour", got)
		}
		if got := rec.Header().Get("anthropic-ratelimit-unified-reset"); got != strconv.FormatInt(target.Unix(), 10) {
			t.Fatalf("reset %q, want %d", got, target.Unix())
		}
	})
}

// Through the full gateway, a Claude subscription 429 passes the unified
// rejection headers past holdWriter without leaking internal notes, and
// keepRetry preserves upstream anthropic-ratelimit-unified-* headers on 429s.
func TestClaude429UnifiedHeadersThroughGatewayAndKeepRetry(t *testing.T) {
	t.Run("keepRetry preserves unified rejection headers", func(t *testing.T) {
		reset := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
		src := http.Header{
			"Anthropic-Ratelimit-Unified-Status":               {"rejected"},
			"Anthropic-Ratelimit-Unified-Reset":                {reset},
			"Anthropic-Ratelimit-Unified-Representative-Claim": {"seven_day"},
			"Anthropic-Ratelimit-Unified-Overage-Status":       {"rejected"},
			"Anthropic-Ratelimit-Unified-7d-Utilization":       {"1"},
			"Anthropic-Ratelimit-Unified-7d-Reset":             {reset},
		}
		dst := http.Header{}
		keepRetry(dst, src, nil)
		for k, vs := range src {
			if got := dst.Get(k); got != vs[0] {
				t.Errorf("%s: %q, want %q", k, got, vs[0])
			}
		}
	})

	t.Run("multi-account pool clamps unified reset to soonest account back", func(t *testing.T) {
		fresh(t)
		now := time.Now()
		soon := now.Add(25 * time.Minute).Unix()
		later := now.Add(3 * time.Hour).Unix()

		upA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Anthropic-Ratelimit-Unified-Status", "rejected")
			w.Header().Set("Anthropic-Ratelimit-Unified-Reset", strconv.FormatInt(soon, 10))
			w.Header().Set("Anthropic-Ratelimit-Unified-Representative-Claim", "five_hour")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"You've hit your 5-hour limit"}}`)
		}))
		defer upA.Close()
		upB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Anthropic-Ratelimit-Unified-Status", "rejected")
			w.Header().Set("Anthropic-Ratelimit-Unified-Reset", strconv.FormatInt(later, 10))
			w.Header().Set("Anthropic-Ratelimit-Unified-Representative-Claim", "five_hour")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"You've hit your 5-hour limit"}}`)
		}))
		defer upB.Close()

		for _, p := range []provider.Provider{
			{ID: "anth-a", Name: "Anth A", Key: "ka", Anthropic: upA.URL, Models: []string{"claude-sonnet-5"}},
			{ID: "anth-b", Name: "Anth B", Key: "kb", Anthropic: upB.URL, Models: []string{"claude-sonnet-5"}},
		} {
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
		}
		if err := provider.SaveGroup(provider.Group{
			ID:      "g-cl",
			Name:    "G CL",
			Routing: "order",
			Members: []string{"anth-a/claude-sonnet-5", "anth-b/claude-sonnet-5"},
		}); err != nil {
			t.Fatal(err)
		}

		s := New()
		srv := httptest.NewServer(s.Handler())
		defer srv.Close()
		res, err := http.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(
			`{"model":"group/g-cl","max_tokens":50,"messages":[{"role":"user","content":"hi"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("status %d, want 429", res.StatusCode)
		}
		if got := res.Header.Get("anthropic-ratelimit-unified-status"); got != "rejected" {
			t.Fatalf("status %q, want rejected (headers %v)", got, res.Header)
		}
		if got := res.Header.Get("anthropic-ratelimit-unified-representative-claim"); got != "five_hour" {
			t.Fatalf("claim %q, want five_hour", got)
		}
		gotReset, err := strconv.ParseInt(res.Header.Get("anthropic-ratelimit-unified-reset"), 10, 64)
		if err != nil {
			t.Fatalf("reset header %q: %v", res.Header.Get("anthropic-ratelimit-unified-reset"), err)
		}
		if diff := gotReset - soon; diff < -5 || diff > 5 {
			t.Fatalf("unified reset %d, want ~%d (soonest account, not later %d)", gotReset, soon, later)
		}
		if res.Header.Get(resetsHeader) != "" {
			t.Fatalf("leaked internal header %s=%q", resetsHeader, res.Header.Get(resetsHeader))
		}
	})

	t.Run("claude subscription 429 through gateway passes unified headers", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("a shell script stands in for Claude Code")
		}
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
		t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
		os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
		os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), mustJSON(map[string]any{"claudeAiOauth": map[string]any{
			"accessToken": "tok-me", "refreshToken": "r-me", "expiresAt": time.Now().Add(24 * time.Hour).UnixMilli(), "subscriptionType": "pro"}}), 0o600)
		os.WriteFile(filepath.Join(home, ".claude.json"), mustJSON(map[string]any{
			"oauthAccount": map[string]any{"emailAddress": "rej-gw@example.com", "accountUuid": "u-rej"}}), 0o600)
		five := strconv.FormatInt(time.Now().Add(2*time.Hour).Unix(), 10)
		dir := t.TempDir()
		testenv.Program(t, filepath.Join(dir, "claude"), `#!/bin/sh
if [ "$1" = auth ]; then echo '{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty","email":"rej-gw@example.com","subscriptionType":"pro"}'; exit 0; fi
while read -r line; do
  echo '{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"five_hour","resetsAt":`+five+`,"overageStatus":"rejected","unifiedWindows":{"five_hour":{"utilization":1,"resetsAt":`+five+`}}}}'
  echo '{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"result":"You'\''ve hit your 5-hour limit · resets 1:20pm (Asia/Taipei)"}'
done
`)
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		provider.ForgetAccounts()
		t.Cleanup(provider.ForgetAccounts)
		s := New()
		t.Cleanup(s.subscription.abortAll)
		srv := httptest.NewServer(s.Handler())
		defer srv.Close()
		for _, stream := range []string{"true", "false"} {
			res, err := http.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(
				`{"model":"claude/claude-sonnet-5","max_tokens":50,"stream":`+stream+`,"messages":[{"role":"user","content":"ping `+stream+`"}]}`))
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode != http.StatusTooManyRequests {
				t.Fatalf("stream=%s: %d %s", stream, res.StatusCode, b)
			}
			for k, v := range map[string]string{
				"anthropic-ratelimit-unified-status":               "rejected",
				"anthropic-ratelimit-unified-reset":                five,
				"anthropic-ratelimit-unified-representative-claim": "five_hour",
				"anthropic-ratelimit-unified-overage-status":       "rejected",
				"anthropic-ratelimit-unified-5h-reset":             five,
			} {
				if got := res.Header.Get(k); got != v {
					t.Errorf("stream=%s: %s %q, want %q (headers %v)", stream, k, got, v, res.Header)
				}
			}
			if res.Header.Get(resetsHeader) != "" {
				t.Errorf("stream=%s: leaked %s=%q", stream, resetsHeader, res.Header.Get(resetsHeader))
			}
		}
	})
}
