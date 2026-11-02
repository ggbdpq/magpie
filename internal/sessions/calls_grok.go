package sessions

import (
	"bytes"
	"cmp"
	"encoding/json"
	"path/filepath"
	"time"
)

// Grok Build's own calls (#4423, liubing0806: its direct calls to x.ai
// never reached Usage) — read from the same updates.jsonl the Sessions
// page reads (parseGrok), each turn_completed's usage a call per model in
// modelUsage, on that model's own counts (grokUsage.tokens: input without
// what the cache held). An empty modelUsage is the session's model
// (current_model_id, else "grok"). The turn's time is the line's. A
// subagent's turns are in its own folder, a call of its own session. A
// fork counts the lines written from its created_at on: what it copied
// from its source is older, and was counted where it was first written.
//
// A call made through magpie's gateway is the gateway's to count; the
// ledger matches these calls with its records as it does the others'
// (gatewayMatches) and leaves the matched out of Local session. The
// turn_completed lines carry no message id, so none is matched on.
//
// Grok Build only appends to updates.jsonl, a line an event, so a file
// that grew is read on from where the last read left off
// (lineCallReaders).

// grokCallFiles are the session files Sessions lists for Grok Build
// (grokFiles): one updates.jsonl per session folder, beside its
// summary.json.
func grokCallFiles() []file { return grokFiles() }

// grokCallCarry is what a read of a session's updates carries on with:
// what of its summary the turns are counted under.
type grokCallCarry struct {
	Since time.Time `json:"since,omitzero"`
	Model string    `json:"model,omitempty"`
}

// grokCallsBegin starts a session's updates read from its start, with its
// summary: the model an empty modelUsage falls back to, and, for a fork,
// the time its source's lines were copied before.
func grokCallsBegin(st *callFile, f file) {
	st.Session = filepath.Base(filepath.Dir(f.path))
	c := &grokCallCarry{}
	if m, ok := readGrokSummary(f.m().manifest); ok {
		c.Model = m.CurrentModel
		if m.Parent != "" {
			// what it copied from its source is older than it
			if at, err := time.Parse(time.RFC3339Nano, m.CreatedAt); err == nil {
				c.Since = at.Truncate(time.Second)
			}
		}
	}
	st.Grok = c
}

// grokCallsKept is whether st holds what reading on needs: a disk shard
// written before the carry was kept there does not, and its file is read
// whole again once it changes.
func grokCallsKept(st *callFile) bool { return st.Grok != nil }

// grokCallLine reads one line of a session's updates: a turn_completed
// with usage is a call per model.
func grokCallLine(st *callFile, b []byte) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || len(b) > maxLine || !bytes.Contains(b, grokTurn) {
		return
	}
	at := numTS(b)
	if at.IsZero() {
		return
	}
	if c := st.Grok; c != nil && !c.Since.IsZero() && at.Before(c.Since) {
		return
	}
	var l grokLine
	if json.Unmarshal(b, &l) != nil {
		return
	}
	u := l.Params.Update
	if u.SessionUpdate != "turn_completed" || u.Usage == nil {
		return
	}
	add := func(model string, u grokUsage) {
		t := u.tokens()
		if t.zero() {
			return
		}
		st.Calls = append(st.Calls, Call{
			Agent:   "grok",
			Time:    at,
			Session: st.Session,
			Model:   st.str(model),
			Tokens:  t,
		})
	}
	if len(u.Usage.ModelUsage) == 0 {
		var model string
		if c := st.Grok; c != nil {
			model = c.Model
		}
		add(cmp.Or(model, "grok"), u.Usage.grokUsage)
		return
	}
	for name, mu := range u.Usage.ModelUsage {
		add(name, mu)
	}
}
