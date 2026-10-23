package gateway

import (
	"context"
	"io"
	"testing"
	"time"
)

// bareCallTurn is a streamed reply whose one call names its tool as told,
// ended tool_use.
func bareCallTurn(id, name string) []string {
	return []string{
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"` + id + `","model":"claude-opus-5-5","usage":{"input_tokens":3,"output_tokens":1}}}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_01Bare","name":"` + name + `","input":{}}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"echo ok\"}"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`,
		`{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":30}}}`,
		`{"type":"stream_event","event":{"type":"message_stop"}}`,
	}
}

// readCalls runs lines through a run's readOutput and says what the client
// was handed, waiting a moment past the last of them.
func readCalls(t *testing.T, run *subscriptionRun, lines []string) (calls []string, args, stop bool) {
	t.Helper()
	segment := run.attach()
	pr, pw := io.Pipe()
	go run.readOutput(pr)
	t.Cleanup(func() { pw.Close() })
	go func() {
		for _, l := range lines {
			pw.Write([]byte(l + "\n"))
		}
	}()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev, ok := <-segment:
			if !ok {
				return
			}
			switch ev.Kind {
			case KToolStart:
				calls = append(calls, ev.Name)
			case KToolArgs:
				args = true
			case KStop:
				stop = ev.Stop == "tool" // the run's shape of stop_reason tool_use
				return
			}
		case <-deadline:
			return
		}
	}
}

// A call of the client's tool under the bare name its instructions teach —
// no mcp__magpie__ before it — is the client's to answer: the client hears
// the tool_use and the reply ends tool_use, as with a call under the name
// it was offered under. The run's Claude Code has none of the client's
// tools (--tools), so a call kept here only had it answer that the tool is
// missing: no tool_use reached the client, no result came back, and the
// turn ended at its first call having run nothing (#3373).
func TestClaudeBareNameCallIsTheClients(t *testing.T) {
	run := &subscriptionRun{tools: map[string]bool{"Bash": true, "Read": true}}
	calls, args, stop := readCalls(t, run, bareCallTurn("msg_bare", "Bash"))
	if len(calls) != 1 || calls[0] != "Bash" || !args || !stop {
		t.Fatalf("the client was shown calls %v (args %v, stop %v), want Bash's call handed over", calls, args, stop)
	}
}

// The same told in whole messages, as Claude Code tells an answer it asked
// for again without a stream.
func TestClaudeBareNameCallUnstreamedIsTheClients(t *testing.T) {
	run := &subscriptionRun{tools: map[string]bool{"Bash": true}}
	lines := []string{
		`{"type":"assistant","message":{"id":"msg_whole","model":"claude-opus-5-5","role":"assistant","content":[{"type":"tool_use","id":"toolu_01Bare","name":"Bash","input":{"command":"echo ok"}}],"stop_reason":"tool_use","usage":{"input_tokens":3,"output_tokens":30}}}`,
	}
	calls, args, stop := readCalls(t, run, lines)
	if len(calls) != 1 || calls[0] != "Bash" || !args || !stop {
		t.Fatalf("the client was shown calls %v (args %v, stop %v), want Bash's call handed over", calls, args, stop)
	}
}

// A tool the client doesn't offer stays Claude Code's own (its WebSearch,
// or a name it was never told): no call goes to the client.
func TestClaudeBareNameCallOfNoClientToolStaysItsOwn(t *testing.T) {
	run := &subscriptionRun{tools: map[string]bool{"Bash": true}}
	calls, _, stop := readCalls(t, run, bareCallTurn("msg_own", "WebSearch"))
	if len(calls) != 0 || stop {
		t.Fatalf("the client was shown %v (stop %v), want none of the run's own call", calls, stop)
	}
}

// Magpie answers the client's web search itself, by the bare name too.
func TestClaudeBareNameSearchStaysMagpies(t *testing.T) {
	run := &subscriptionRun{
		tools:      map[string]bool{"web_search": true},
		search:     func(context.Context, string) (string, []Hit, error) { return "hits", nil, nil },
		searchName: "web_search",
	}
	calls, _, stop := readCalls(t, run, bareCallTurn("msg_search", "web_search"))
	if len(calls) != 0 || stop {
		t.Fatalf("the client was shown %v (stop %v), want magpie's own search kept", calls, stop)
	}
}
