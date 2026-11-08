package usage

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// A Claude call through the gateway and the session file's copy of that
// call share no request id: Claude Code's file names the API's message id,
// which the gateway recorded as the reply's id, and names no request id of
// the gateway's own (#5674). Matching the two logs by that id tells one
// call, where the ±2s fallback alone matched neither a copy whose end lies
// beyond the window (#5674, case A) nor one whose token counts differ
// (#5674, case B), and left it behind as a second, local call.
func claudePair(at time.Time, localEnd time.Duration, gwOut, localOut int) ([]Record, []sessions.Call) {
	recs := []Record{{
		Time: at, Agent: "claude", Provider: "anthropic", Model: "claude-sonnet-4-5",
		Input: 100, Output: gwOut, Millis: 1000, Status: 200,
		RequestID: "mgp-1", ResponseID: "msg_abc", Session: "s",
	}}
	logs := []sessions.Call{{
		Time: at.Add(localEnd), Agent: "claude", Session: "s", Model: "claude-sonnet-4-5",
		Tokens: sessions.Tokens{Input: 100, Output: localOut}, Millis: 1000, Msg: "msg_abc",
	}}
	return recs, logs
}

// The gateway's reply id and the session file's message id are one call's
// two names: matched even where the ±2s window's fallback is not, and
// counted once, not once through magpie and once as a local call.
func TestClaudeMessageIDMatchesGatewayResponseID(t *testing.T) {
	now := time.Date(2026, 10, 1, 13, 0, 0, 0, time.UTC)
	recs, logs := claudePair(now, 5*time.Second, 50, 50) // case A: 5s apart, the same tokens
	matched := gatewayMatches(recs, logs)
	if !matched[0] {
		t.Fatal("the file's call of the reply's id was left as a second, local call")
	}
	rows, _, _, _ := ledgerWith(time.Time{}, Filter{}, recs, logs)
	if len(rows) != 1 {
		t.Fatalf("one call told %d rows apart", len(rows))
	}
	if len(rows) == 1 && rows[0].Source == "log" {
		t.Fatal("the gateway's own call was read away as a local one")
	}
}

// The same reply id tells the call though its two logs split the tokens
// differently, as the window's fallback, which matches on the counts, did
// not.
func TestClaudeMessageIDMatchesDespiteTokenMismatch(t *testing.T) {
	now := time.Date(2026, 10, 1, 13, 0, 0, 0, time.UTC)
	recs, logs := claudePair(now, time.Second, 50, 60) // case B: within the window, tokens apart
	matched := gatewayMatches(recs, logs)
	if !matched[0] {
		t.Fatal("the file's call of the reply's id was left as a second, local call")
	}
	rows, _, _, _ := ledgerWith(time.Time{}, Filter{}, recs, logs)
	if len(rows) != 1 {
		t.Fatalf("one call told %d rows apart", len(rows))
	}
}

// A call the gateway and the file name by the same request id is matched as
// it was, whatever the reply ids say.
func TestCodexRequestIDMatchUnchanged(t *testing.T) {
	now := time.Date(2026, 10, 1, 13, 0, 0, 0, time.UTC)
	recs := []Record{{
		Time: now, Agent: "codex", Provider: "openai", Model: "gpt-5.2",
		Input: 10, Output: 2, Millis: 1000, Status: 200, RequestID: "req-1",
	}}
	logs := []sessions.Call{{
		Time: now.Add(time.Second), Agent: "codex", Session: "native", Model: "gpt-5.2",
		Tokens: sessions.Tokens{Input: 10, Output: 2}, Millis: 1000, RequestID: "req-1",
	}}
	if !gatewayMatches(recs, logs)[0] {
		t.Fatal("the request id's match was lost")
	}
	// Codex's own pairing, by the reply's id, is Codex's still, and the
	// message id a Claude call names is no Codex reply's.
	pairs := codexResponseMatches(recs, logs, nil)
	if len(pairs) != 0 {
		t.Fatal("a call with no reply id of its own was paired by one")
	}
	recs[0].ResponseID = "resp-1"
	logs[0].ResponseID = "resp-1"
	if pairs = codexResponseMatches(recs, logs, nil); len(pairs) != 1 {
		t.Fatal("Codex's reply-id pairing was lost")
	}
	if !gatewayMatches(recs, logs)[0] {
		t.Fatal("Codex's reply-id pairing stopped matching the call")
	}
}

// A call neither log names an id of is matched as it was, by its session,
// tokens and end within the window.
func TestNoIDFallbackMatchUnchanged(t *testing.T) {
	now := time.Date(2026, 10, 1, 13, 0, 0, 0, time.UTC)
	recs := []Record{{
		Time: now, Agent: "claude", Provider: "anthropic", Model: "claude-sonnet-4-5",
		Input: 100, Output: 50, Millis: 1000, Status: 200, Session: "s",
	}}
	logs := []sessions.Call{{
		Time: now.Add(time.Second), Agent: "claude", Session: "s", Model: "claude-sonnet-4-5",
		Tokens: sessions.Tokens{Input: 100, Output: 50}, Millis: 1000,
	}}
	if !gatewayMatches(recs, logs)[0] {
		t.Fatal("the window's fallback match was lost")
	}
	recs[0].Time = now.Add(5 * time.Second)
	if gatewayMatches(recs, logs)[0] {
		t.Fatal("a call 5s apart matched without an id or the window")
	}
}

// The page's own pairing matches the two names too, and reads the file it
// would have skipped: the chunk's message ids are among what the gateway's
// reply ids name.
func TestClaudeMessageIDMatchOpensNamedChunk(t *testing.T) {
	now := time.Date(2026, 10, 1, 13, 0, 0, 0, time.UTC)
	recs, logs := claudePair(now, 5*time.Second, 50, 50)
	gateway := &rowChunk{}
	gateway.add(Row{Record: recs[0]}, "", 0, false)
	local := &rowChunk{Source: sessions.CallSource{Path: "/local/cc-session"}}
	local.add(Row{Record: logRecord(logs[0]), Source: "log"}, logs[0].Msg, 0, false)
	local.serial = nextChunkSerial()
	var opened []*rowChunk
	matchOpening = func(c *rowChunk) { opened = append(opened, c) }
	t.Cleanup(func() { matchOpening = nil })
	ps := pairRows(All, now, []*rowChunk{gateway}, []*rowChunk{local}, nil, nil)
	matched := false
	for chunk := range ps.matched {
		if chunk.Source.Path == "/local/cc-session" && ps.matched.has(rowRef{chunk, 0}) {
			matched = true
		}
	}
	if !matched {
		t.Fatal("the page's pairing left the file's call as a second, local one")
	}
	if len(opened) == 0 {
		t.Fatal("matching never read the file whose message id the gateway's reply names")
	}
}
