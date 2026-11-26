package sessions

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// A Codex fork (the CLI's /fork, the app's fork, a spawn_agent with the
// parent's context) starts a rollout of its own and writes every item of
// its parent's into it first, token_count events included, as they were
// (codex-rs record_initial_history, InitialHistory::Forked). Each copied
// line is stamped when the fork wrote it, so a copied checkpoint is the
// parent's call again, at the fork's time, and read as one it counted the
// parent's whole history a second time on the day of the fork (Sky,
// skyhuang1994: a week of 5.1B gpt-5.4 tokens where the machine had spent
// ~0.27B). The fork's own session_meta names the parent (forked_from_id),
// and a checkpoint the parent's rollout holds, its running total and its
// last call alike, is a copy: the parent's file counts it. A fork of a fork
// holds its grandparent's lines through its parent's file, which holds
// them too. When the parent's rollout can't be found or read, the fork's
// lines are read as before.
//
// The copies are the head of the fork's rollout and nothing after it: a
// fork goes on working, and its own calls are its own whatever their
// numbers (Sky, skyhuang1994: forks that went on working lost their own
// usage). The fork's first turn the parent never had, a turn_id of its
// own, ends the copies. A rollout with no turn ids is matched by
// checkpoint alone: its running total, which a fork's own call moves past
// the parent's, keeps a later call of either from matching.

// cxCheckpoint is a token_count event's two counters, as written.
type cxCheckpoint struct{ Total, Last cxUsage }

// forkedFrom notes, from the first session_meta of the rollout at path,
// the parent the rollout was forked from, and reads that parent's
// checkpoints (across all paginated rollout files and ancestor forks up to
// codexForkMaxDepth, so a depth-2 fork whose parent was compacted or
// paginated still deduplicates its grandparent's checkpoints, #7818).
func (s *codexUsageState) forkedFrom(path, parent string) {
	if parent == "" || path == "" {
		return
	}
	const maxDepth = 8
	seen := map[string]bool{}
	queue := []string{parent}
	var allMarks map[cxCheckpoint]bool
	var allTurns map[string]bool
	for depth := 0; depth < maxDepth && len(queue) > 0; depth++ {
		var next []string
		for _, id := range queue {
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			for _, p := range codexRolloutsOf(path, id) {
				marks, turns, up := codexCheckpointsOf(p)
				for k := range marks {
					if allMarks == nil {
						allMarks = map[cxCheckpoint]bool{}
					}
					allMarks[k] = true
				}
				for k := range turns {
					if allTurns == nil {
						allTurns = map[string]bool{}
					}
					allTurns[k] = true
				}
				if up != "" && !seen[up] {
					next = append(next, up)
				}
			}
		}
		queue = next
	}
	s.Copied, s.CopiedTurns = allMarks, allTurns
}

// ownTurn ends the copies at a turn of the fork's own. A turn id of
// "rollout-<n>" is not one: codex-rs gives that name to a turn a rollout
// line belongs to when it has no id of its own (the rollout canonicalizer's
// implicit turns, the thread-history builder), where a parent's own ids are
// uuids. A fork's copied head can hold such a marker the parent's file
// doesn't, and ending the copies there booked every copied checkpoint after
// it as the fork's call (#5090).
func (s *codexUsageState) ownTurn(turn string) {
	if strings.HasPrefix(turn, "rollout-") {
		return
	}
	if turn != "" && s.Copied != nil && s.CopiedTurns != nil && !s.CopiedTurns[turn] {
		s.Copied, s.CopiedTurns = nil, nil
	}
}

// copied says whether a checkpoint is one of the parent's, copied in.
func (s *codexUsageState) copied(total, last *cxUsage) bool {
	if len(s.Copied) == 0 || total == nil || last == nil {
		return false
	}
	return s.Copied[cxCheckpoint{*total, *last}]
}

// codexForks keeps the checkpoints of the parents last read, by path,
// size and time: forks a spawn_agent makes are of one parent, often many
// at a time.
var codexForks struct {
	sync.Mutex
	kept []codexForkParent
}

type codexForkParent struct {
	path   string
	size   int64
	mod    time.Time
	marks  map[cxCheckpoint]bool
	turns  map[string]bool
	parent string
}

const codexForksKept = 8

// codexCheckpointsOf are the token_count checkpoints of the rollout at
// path, its turn ids (nil when it names none), and its own forked_from_id
// if any; nil when it can't be read whole.
func codexCheckpointsOf(path string) (map[cxCheckpoint]bool, map[string]bool, string) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, nil, ""
	}
	codexForks.Lock()
	for _, k := range codexForks.kept {
		if k.path == path && k.size == fi.Size() && k.mod.Equal(fi.ModTime()) {
			codexForks.Unlock()
			return k.marks, k.turns, k.parent
		}
	}
	codexForks.Unlock()
	marks := map[cxCheckpoint]bool{}
	var turns map[string]bool
	var parent string
	sawMeta := false
	err = eachLine(path, func(b []byte) {
		h := b[:min(len(b), 1024)]
		top, payload := typeAfter(h, cxType), typeAfter(h, cxPayload)
		if top == "session_meta" && !sawMeta {
			sawMeta = true
			var l struct {
				Payload struct {
					ForkedFrom string `json:"forked_from_id"`
				} `json:"payload"`
			}
			if json.Unmarshal(b, &l) == nil {
				parent = l.Payload.ForkedFrom
			}
			return
		}
		if top == "turn_context" || top == "event_msg" && payload == "task_started" {
			var l struct {
				Payload struct {
					TurnID string `json:"turn_id"`
				} `json:"payload"`
			}
			if json.Unmarshal(b, &l) == nil && l.Payload.TurnID != "" {
				if turns == nil {
					turns = map[string]bool{}
				}
				turns[l.Payload.TurnID] = true
			}
			return
		}
		if top != "event_msg" || payload != "token_count" || bytes.Contains(h, cxInfo) {
			return
		}
		total, ok := cxUsageIn(b, cxTotal)
		if !ok {
			return
		}
		if last, ok := cxUsageIn(b, cxLast); ok {
			marks[cxCheckpoint{*total, *last}] = true
		}
	})
	if err != nil {
		return nil, nil, ""
	}
	codexForks.Lock()
	codexForks.kept = append(codexForks.kept, codexForkParent{path, fi.Size(), fi.ModTime(), marks, turns, parent})
	if n := len(codexForks.kept); n > codexForksKept {
		codexForks.kept = append([]codexForkParent(nil), codexForks.kept[n-codexForksKept:]...)
	}
	codexForks.Unlock()
	return marks, turns, parent
}

// codexRolloutsOf are all rollout files of thread id in the Codex folder that
// holds the rollout at path (including paginated pages). Codex files a rollout
// under sessions/YYYY/MM/DD, the local day it began, and a thread id is a
// UUIDv7, which carries that time: the days around it are looked in
// first, then every day, then archived_sessions.
func codexRolloutsOf(path, id string) []string {
	home := codexHomeOf(path)
	if home == "" || strings.ContainsAny(id, `/\*?[`) {
		return nil
	}
	name := "rollout-*-" + id + "*.jsonl*"
	seen := map[string]bool{}
	var out []string
	collect := func(pattern string) {
		found, _ := filepath.Glob(pattern)
		for _, p := range found {
			if seen[p] || p == path {
				continue
			}
			if m := rolloutName.FindStringSubmatch(filepath.Base(p)); m != nil && m[1] == id {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	sessions := filepath.Join(home, "sessions")
	if at, ok := uuidV7Time(id); ok {
		for _, d := range []int{0, -1, 1} {
			day := at.AddDate(0, 0, d).Local()
			collect(filepath.Join(sessions, day.Format("2006"), day.Format("01"), day.Format("02"), name))
		}
	}
	if len(out) == 0 {
		collect(filepath.Join(sessions, "*", "*", "*", name))
	}
	collect(filepath.Join(home, "archived_sessions", name))
	return out
}

// uuidV7Time is the time a UUIDv7 was made, its first 48 bits.
func uuidV7Time(id string) (time.Time, bool) {
	h := strings.ReplaceAll(id, "-", "")
	if len(h) != 32 || h[12] != '7' {
		return time.Time{}, false
	}
	b, err := hex.DecodeString(h[:12])
	if err != nil {
		return time.Time{}, false
	}
	var ms int64
	for _, c := range b {
		ms = ms<<8 | int64(c)
	}
	return time.UnixMilli(ms), true
}
