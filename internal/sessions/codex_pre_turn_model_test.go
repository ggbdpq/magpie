package sessions

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Early Codex usage events recorded before the first turn_context (a guardian
// subagent's leading compacted snapshot, or an orphaned fork's copied
// token_count) must be backfilled with the file's first named model instead
// of remaining model="" in Calls and List (#7818).
func TestCodexPreTurnCompactionAndTokenCountBackfillModel(t *testing.T) {
	t.Run("guardian_compacted_before_turn_context", func(t *testing.T) {
		d := setupCalls(t)
		const id = "019d2000-a100-7aaa-8bbb-cccdddeee001"
		lines := []string{
			fmt.Sprintf(`{"timestamp":"2026-09-10T02:38:05.281Z","type":"session_meta","payload":{"id":%q,"timestamp":"2026-09-10T02:38:05.281Z","cwd":"/w/proj","source":{"subagent":{"other":"guardian"}},"model_provider":"openai"}}`, id),
			`{"timestamp":"2026-09-10T02:38:05.281Z","type":"compacted","payload":{"compaction_response_id":"resp_comp_1","latest_token_usage_record":{"response_id":"resp_comp_1","thread_id":"` + id + `","usage":{"input_tokens":251432,"cached_input_tokens":4864,"output_tokens":1473,"reasoning_output_tokens":0,"total_tokens":252905},"thread_token_usage":{"input_tokens":2946985,"cached_input_tokens":2395392,"output_tokens":3859,"reasoning_output_tokens":0,"total_tokens":2950844}}}}`,
			`{"timestamp":"2026-09-10T02:38:05.281Z","type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{}}}`,
			`{"timestamp":"2026-09-10T02:38:05.281Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":2695553,"cached_input_tokens":2390528,"output_tokens":2386,"reasoning_output_tokens":0,"total_tokens":2697939},"last_token_usage":{"input_tokens":0,"cached_input_tokens":0,"output_tokens":0,"reasoning_output_tokens":0,"total_tokens":0}}}}`,
			`{"timestamp":"2026-09-10T02:38:05.281Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}`,
			`{"timestamp":"2026-09-10T02:38:05.281Z","type":"turn_context","payload":{"turn_id":"t1","cwd":"/w/proj","model":"codex-auto-review","effort":"medium"}}`,
			`{"timestamp":"2026-09-10T02:38:10.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":2705553,"cached_input_tokens":2395528,"output_tokens":2586,"reasoning_output_tokens":0,"total_tokens":2708139},"last_token_usage":{"input_tokens":10000,"cached_input_tokens":5000,"output_tokens":200,"reasoning_output_tokens":0,"total_tokens":10200}}}}`,
		}
		writeLines(t, filepath.Join(d.codex, "sessions", "2026", "09", "10", "rollout-2026-09-10T02-38-05-"+id+".jsonl"), lines...)

		cs := Calls(time.Time{})
		if len(cs) != 2 {
			t.Fatalf("got %d calls, want 2: %+v", len(cs), cs)
		}
		for i, c := range cs {
			if c.Model != "codex-auto-review" {
				t.Errorf("call[%d].Model = %q, want codex-auto-review", i, c.Model)
			}
		}
		ss := List(0)
		if len(ss) != 1 {
			t.Fatalf("got %d sessions, want 1", len(ss))
		}
		for _, m := range ss[0].Models {
			if m.Model == "" {
				t.Errorf("session summary still has empty model bucket: %+v", ss[0].Models)
			}
			if m.Model != "codex-auto-review" {
				t.Errorf("unexpected model in summary: %+v", m)
			}
		}
	})

	t.Run("orphaned_fork_token_count_before_turn_context", func(t *testing.T) {
		d := setupCalls(t)
		const id = "019d2000-a200-7aaa-8bbb-cccdddeee002"
		const missingParent = "019d1fff-0000-7aaa-8bbb-cccdddeee000"
		lines := []string{
			fmt.Sprintf(`{"timestamp":"2026-07-23T03:28:54.812Z","type":"session_meta","payload":{"id":%q,"forked_from_id":%q,"timestamp":"2026-07-23T03:28:54.812Z","cwd":"/w/proj","source":{"subagent":{"thread_spawn":{"parent_thread_id":%q,"depth":2}}},"model_provider":"openai"}}`, id, missingParent, missingParent),
			`{"timestamp":"2026-07-23T03:28:54.813Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":30783290,"cached_input_tokens":30000000,"output_tokens":2180,"reasoning_output_tokens":0,"total_tokens":30785470},"last_token_usage":{"input_tokens":179015,"cached_input_tokens":177920,"output_tokens":218,"reasoning_output_tokens":0,"total_tokens":179233}}}}`,
			`{"timestamp":"2026-07-23T03:29:01.212Z","type":"turn_context","payload":{"turn_id":"t_own","cwd":"/w/proj","model":"gpt-5.6-sol","effort":"high"}}`,
			`{"timestamp":"2026-07-23T03:29:05.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":30803290,"cached_input_tokens":30010000,"output_tokens":2380,"reasoning_output_tokens":0,"total_tokens":30805670},"last_token_usage":{"input_tokens":20000,"cached_input_tokens":10000,"output_tokens":200,"reasoning_output_tokens":0,"total_tokens":20200}}}}`,
		}
		writeLines(t, filepath.Join(d.codex, "sessions", "2026", "07", "23", "rollout-2026-07-23T03-28-54-"+id+".jsonl"), lines...)

		cs := Calls(time.Time{})
		if len(cs) != 2 {
			t.Fatalf("got %d calls, want 2: %+v", len(cs), cs)
		}
		for i, c := range cs {
			if c.Model != "gpt-5.6-sol" {
				t.Errorf("call[%d].Model = %q, want gpt-5.6-sol", i, c.Model)
			}
		}
		ss := List(0)
		if len(ss) != 1 || len(ss[0].Models) != 1 || ss[0].Models[0].Model != "gpt-5.6-sol" {
			t.Fatalf("unexpected session models: %+v", ss)
		}
	})
}

// A depth-2 fork whose depth-1 parent was forked from a root session (or
// paginated across multiple rollout files) must deduplicate copied checkpoints
// from both its parent's pages and its grandparent's rollout (#7818).
func TestCodexDepth2ForkDeduplicatesGrandparentAndPaginatedParent(t *testing.T) {
	d := setupCalls(t)
	const (
		root   = "019d186b-a100-7aaa-8bbb-cccdddeee100"
		depth1 = "019d186b-a200-7aaa-8bbb-cccdddeee200"
		depth2 = "019d186b-a300-7aaa-8bbb-cccdddeee300"
	)
	dayDir := filepath.Join(d.codex, "sessions", "2026", "03", "23")

	// Root holds checkpoint C1.
	writeLines(t, filepath.Join(dayDir, "rollout-2026-03-23T10-00-00-"+root+".jsonl"),
		fmt.Sprintf(`{"timestamp":"2026-03-23T10:00:00.000Z","type":"session_meta","payload":{"id":%q,"cwd":"/w/proj","model_provider":"openai"}}`, root),
		`{"timestamp":"2026-03-23T10:00:01.000Z","type":"turn_context","payload":{"turn_id":"t_root","cwd":"/w/proj","model":"gpt-5.6-sol"}}`,
		`{"timestamp":"2026-03-23T10:00:02.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":10000,"cached_input_tokens":8000,"output_tokens":100,"reasoning_output_tokens":0,"total_tokens":10100},"last_token_usage":{"input_tokens":10000,"cached_input_tokens":8000,"output_tokens":100,"reasoning_output_tokens":0,"total_tokens":10100}}}}`,
	)

	// Depth-1 forked from root, paginated across two files: page 1 has only
	// session_meta, page 2 holds depth-1's own checkpoint C2.
	writeLines(t, filepath.Join(dayDir, "rollout-2026-03-23T10-01-00-"+depth1+".jsonl"),
		fmt.Sprintf(`{"timestamp":"2026-03-23T10:01:00.000Z","type":"session_meta","payload":{"id":%q,"forked_from_id":%q,"cwd":"/w/proj","model_provider":"openai"}}`, depth1, root),
	)
	writeLines(t, filepath.Join(dayDir, "rollout-2026-03-23T10-02-00-"+depth1+"_page2.jsonl"),
		fmt.Sprintf(`{"timestamp":"2026-03-23T10:02:00.000Z","type":"session_meta","payload":{"id":%q,"forked_from_id":%q,"cwd":"/w/proj","model_provider":"openai"}}`, depth1, root),
		`{"timestamp":"2026-03-23T10:02:01.000Z","type":"turn_context","payload":{"turn_id":"t_d1","cwd":"/w/proj","model":"gpt-5.6-sol"}}`,
		`{"timestamp":"2026-03-23T10:02:02.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":25000,"cached_input_tokens":20000,"output_tokens":300,"reasoning_output_tokens":0,"total_tokens":25300},"last_token_usage":{"input_tokens":15000,"cached_input_tokens":12000,"output_tokens":200,"reasoning_output_tokens":0,"total_tokens":15200}}}}`,
	)

	// Depth-2 forked from depth-1, starts with copied C1 and C2 BEFORE its
	// first turn_context, then runs its own turn t_d2 (C3).
	writeLines(t, filepath.Join(dayDir, "rollout-2026-03-23T10-03-00-"+depth2+".jsonl"),
		fmt.Sprintf(`{"timestamp":"2026-03-23T10:03:00.000Z","type":"session_meta","payload":{"id":%q,"forked_from_id":%q,"cwd":"/w/proj","source":{"subagent":{"thread_spawn":{"parent_thread_id":%q,"depth":2}}},"model_provider":"openai"}}`, depth2, depth1, depth1),
		`{"timestamp":"2026-03-23T10:03:00.001Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":10000,"cached_input_tokens":8000,"output_tokens":100,"reasoning_output_tokens":0,"total_tokens":10100},"last_token_usage":{"input_tokens":10000,"cached_input_tokens":8000,"output_tokens":100,"reasoning_output_tokens":0,"total_tokens":10100}}}}`,
		`{"timestamp":"2026-03-23T10:03:00.002Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":25000,"cached_input_tokens":20000,"output_tokens":300,"reasoning_output_tokens":0,"total_tokens":25300},"last_token_usage":{"input_tokens":15000,"cached_input_tokens":12000,"output_tokens":200,"reasoning_output_tokens":0,"total_tokens":15200}}}}`,
		`{"timestamp":"2026-03-23T10:03:05.000Z","type":"turn_context","payload":{"turn_id":"t_d2","cwd":"/w/proj","model":"gpt-5.6-sol"}}`,
		`{"timestamp":"2026-03-23T10:03:06.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":31000,"cached_input_tokens":25000,"output_tokens":450,"reasoning_output_tokens":0,"total_tokens":31450},"last_token_usage":{"input_tokens":6000,"cached_input_tokens":5000,"output_tokens":150,"reasoning_output_tokens":0,"total_tokens":6150}}}}`,
	)

	var d2Calls []Call
	for _, c := range Calls(time.Time{}) {
		if strings.Contains(c.File, depth2) {
			d2Calls = append(d2Calls, c)
		}
	}
	if len(d2Calls) != 1 {
		t.Fatalf("depth2 booked %d calls, want 1 own call: %+v", len(d2Calls), d2Calls)
	}
	want := Tokens{Input: 1000, CacheRead: 5000, Output: 150}
	if d2Calls[0].Tokens != want || d2Calls[0].Model != "gpt-5.6-sol" {
		t.Fatalf("depth2 call = %+v, want tokens %+v model gpt-5.6-sol", d2Calls[0], want)
	}
}
