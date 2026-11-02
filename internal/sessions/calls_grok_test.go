package sessions

import (
	"path/filepath"
	"testing"
	"time"
)

// #4423 (liubing0806): Grok Build's own calls reach Usage, read from the
// same updates.jsonl the Sessions page reads (parseGrok). The four sessions
// in testdata/grok:
//
//   - a: two turns, the first spent by grok-4.7-build and grok-4.7-mini
//     together, the second by grok-4.7-build alone
//   - b: a subagent's own folder (session_kind subagent), its one turn its
//     own, not its parent a's
//   - c: a fork of a, whose first turn is the copy of a's first one —
//     counted where it was first written, so only its own turn (written
//     after its created_at) is a call of its own
//   - d: no turn completed, no calls
type grokTotal struct{ n, input, cacheRead, cacheWrite, output int }

func grokCallsBy(f func(Call) string) map[string]grokTotal {
	out := map[string]grokTotal{}
	for _, c := range Calls(time.Time{}) {
		if c.Agent != "grok" {
			continue
		}
		t := out[f(c)]
		t.n, t.input, t.cacheRead, t.cacheWrite, t.output =
			t.n+1, t.input+c.Input, t.cacheRead+c.CacheRead, t.cacheWrite+c.CacheWrite, t.output+c.Output
		out[f(c)] = t
	}
	return out
}

func TestGrokCalls(t *testing.T) {
	for _, viaEnv := range []bool{false, true} {
		name := "~/.grok"
		if viaEnv {
			name = "GROK_HOME"
		}
		t.Run(name, func(t *testing.T) {
			claude, _ := setup(t)
			dir := filepath.Join(filepath.Dir(claude), "home", ".grok")
			copyTree(t, "testdata/grok", dir)
			if viaEnv {
				t.Setenv("GROK_HOME", dir)
			}
			want := map[string]grokTotal{
				"0190aaaa-0000-7000-8000-00000000000a": {3, 800, 2800, 0, 110},
				"0190aaaa-0000-7000-8000-00000000000b": {1, 200, 100, 0, 20},
				"0190aaaa-0000-7000-8000-00000000000c": {1, 100, 300, 0, 30},
			}
			got := grokCallsBy(func(c Call) string { return c.Session })
			if len(got) != len(want) {
				t.Fatalf("sessions with calls %v, want %v", got, want)
			}
			for s, w := range want {
				if got[s] != w {
					t.Fatalf("session %s calls %+v, want %+v", s, got[s], w)
				}
			}
			// the Usage ledger lists them by model
			byModel := grokCallsBy(func(c Call) string { return c.Model })
			wantModels := map[string]grokTotal{
				"grok-4.7-build": {3, 800, 3100, 0, 130},
				"grok-4.7-mini":  {2, 300, 100, 0, 30},
			}
			for m, w := range wantModels {
				if byModel[m] != w {
					t.Fatalf("model %s calls %+v, want %+v", m, byModel[m], w)
				}
			}
			if len(byModel) != len(wantModels) {
				t.Fatalf("models %v, want %v", byModel, wantModels)
			}
		})
	}
}

// A fork counts only its own turns: c's call is its turn of 09:00:30, not
// the copied one of 08:00:20.
func TestGrokCallsForkKeepsItsOwnTurns(t *testing.T) {
	claude, _ := setup(t)
	dir := filepath.Join(filepath.Dir(claude), "home", ".grok")
	copyTree(t, "testdata/grok", dir)
	var got []int64
	for _, c := range Calls(time.Time{}) {
		if c.Agent == "grok" && c.Session == "0190aaaa-0000-7000-8000-00000000000c" {
			got = append(got, c.Time.Unix())
		}
	}
	if len(got) != 1 || got[0] != 1790499630 {
		t.Fatalf("fork c call times %v, want [1790499630]", got)
	}
}
