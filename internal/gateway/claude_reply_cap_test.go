package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

// The client named a reply cap (max_output_tokens) and the model is on the
// Claude subscription, whose bridge's Claude Code takes no such field: the
// cap went nowhere and the whole reply was made, told completed (#5469
// frostming: 32 asked, 1119 came back, status completed). The bridge stops
// the reply's words here once their estimate reaches the cap, and ends the
// reply as one cut at its length, the same incomplete a vendor's own cut
// is.
func TestClaudeReplyStopsAtTheClientCap(t *testing.T) {
	long := strings.Repeat("ocean wave ", 40) // 440 bytes, far past a 32-token cap
	deltas := []string{long[:110], long[110:220], long[220:330], long[330:]}
	lines := []string{
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_cap","model":"claude-opus-5-5","usage":{"input_tokens":9,"output_tokens":1}}}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}`,
	}
	for _, part := range deltas {
		b, _ := json.Marshal(part)
		lines = append(lines, `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":`+string(b)+`}}}`)
	}
	lines = append(lines,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`,
		`{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1119}}}`,
		`{"type":"stream_event","event":{"type":"message_stop"}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":`+func() string { b, _ := json.Marshal(long); return string(b) }()+`}`,
	)
	run := &subscriptionRun{replyCap: 32}
	text, stop, _, _, raw, ended, ok := claudeReply(t, run, lines, true)
	if stop != "max_tokens" || ended != "length" || !ok {
		t.Fatalf("stop %q, run ended %q %v:\n%s", stop, ended, ok, raw)
	}
	// the estimate is len/4, magpie's own count of a request's tokens: the
	// words stop at the cap's worth of bytes, the rest of the attempt and
	// its end_turn are not the client's to hear
	if want := long[:32*4]; text != want {
		t.Fatalf("reply %d bytes, want the %d the cap leaves room for:\n%s", len(text), len(want), raw)
	}
}
