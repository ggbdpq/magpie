package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// claudeServedReply runs lines through readOutput and relay, and returns
// the usage the call was given.
func claudeServedReply(t *testing.T, stream bool, lines []string) Usage {
	t.Helper()
	run := &subscriptionRun{}
	segment := run.attach()
	go run.readOutput(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	rec := httptest.NewRecorder()
	var usage Usage
	code, msg := relay(rec, httptest.NewRequest("POST", "/v1/messages", nil), provider.Anthropic, "Claude Code",
		&Request{Model: "claude-sonnet-5-5", Stream: stream}, segment, &usage,
		func() {}, nil, func(string, string, bool) {})
	if code != 200 || msg != "" {
		t.Fatalf("reply: %d %s", code, msg)
	}
	return usage
}

// claudeServedCases runs a reply through both ways a client may ask, and
// wants the usage's Served served.
func claudeServedCases(t *testing.T, lines []string, want string) {
	t.Helper()
	for _, c := range []struct {
		name   string
		stream bool
	}{{"stream", true}, {"whole", false}} {
		t.Run(c.name, func(t *testing.T) {
			if got := claudeServedReply(t, c.stream, lines).Served; got != want {
				t.Fatalf("served %q, want %q", got, want)
			}
		})
	}
}

// A reply whose messages name no model left the call's usage without
// Served: the routing row read as a 200 that nothing answered, its reader
// unable to tell what replied (#4533). The model asked for is recorded
// instead, as far as the reply names nothing else.
func TestClaudeReplyWithoutModelRecordsServed(t *testing.T) {
	// told whole, as a reply Claude Code tells without a stream is
	claudeServedCases(t, []string{
		`{"type":"assistant","message":{"id":"msg_1","role":"assistant","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":300}}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"hello"}`,
	}, "claude-sonnet-5-5")
}

// The same through a stream whose message_start names no model.
func TestClaudeStreamWithoutModelRecordsServed(t *testing.T) {
	claudeServedCases(t, []string{
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_2","usage":{"input_tokens":10,"output_tokens":5}}}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`,
		`{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":300}}}`,
		`{"type":"stream_event","event":{"type":"message_stop"}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"hello"}`,
	}, "claude-sonnet-5-5")
}

// A reply that does name its model keeps it: the model asked for fills an
// empty served alone.
func TestClaudeReplyModelKept(t *testing.T) {
	claudeServedCases(t, []string{
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_3","model":"claude-opus-5-5","usage":{"input_tokens":10,"output_tokens":5}}}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`,
		`{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":300}}}`,
		`{"type":"stream_event","event":{"type":"message_stop"}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"hello"}`,
	}, "claude-opus-5-5")
}
