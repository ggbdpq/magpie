package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// tuiCalls is a run in Claude Code's interactive mode, its transcript's
// lines written by the test, and its client's tools called through the
// bridge's MCP handler as Claude Code's MCP helper calls them.
type tuiCalls struct {
	t      *testing.T
	run    *subscriptionRun
	bridge *subscriptionBridge
	out    *io.PipeWriter
	called map[string]chan struct{}
}

func newTUICalls(t *testing.T) *tuiCalls {
	t.Helper()
	for v, d := range map[*time.Duration]time.Duration{&callFirst: 50 * time.Millisecond, &unstreamedQuiet: 50 * time.Millisecond} {
		was := *v
		*v = d
		t.Cleanup(func() { *v = was })
	}
	run := &subscriptionRun{tui: true, model: "claude-haiku-5-5", pending: map[string]chan mcpToolResult{}}
	c := &tuiCalls{t: t, run: run, bridge: &subscriptionBridge{runs: map[string]*subscriptionRun{"run": run}, calls: map[string]*subscriptionRun{}}, called: map[string]chan struct{}{}}
	run.attach() // the first reply's
	pr, pw := io.Pipe()
	c.out = pw
	t.Cleanup(func() { pw.Close() })
	go run.readOutput(pr)
	for deadline := time.Now().Add(5 * time.Second); ; {
		run.mu.Lock()
		set := run.onCall != nil
		run.mu.Unlock()
		if set {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the reader never took the run's calls")
		}
		time.Sleep(time.Millisecond)
	}
	return c
}

// write writes transcript lines, as the helper translates them.
func (c *tuiCalls) write(lines ...string) {
	for _, l := range lines {
		c.out.Write([]byte(l + "\n"))
	}
}

// call makes Claude Code's call of a client's tool; it returns once the
// client's result is in. args "" makes the call carry no arguments field
// at all, as it does while its input is still being written (#4611).
func (c *tuiCalls) call(id, args string) {
	done := make(chan struct{})
	c.called[id] = done
	// a call the test never answers ends with the test, not the package
	ctx, cancel := context.WithCancel(context.Background())
	c.t.Cleanup(func() { cancel(); <-done })
	go func() {
		defer close(done)
		body := `{"tool_call_id":"` + id + `","name":"Read"`
		if args != "" {
			body += `,"arguments":` + args
		}
		body += `}`
		r := httptest.NewRequestWithContext(ctx, "POST", "/", strings.NewReader(body))
		r.SetPathValue("token", "run")
		c.bridge.mcpCall(httptest.NewRecorder(), r)
	}()
}

// answer gives the client's result of a call, as deliver does: the next
// reply's segment first.
func (c *tuiCalls) answer(id string) chan Event {
	c.t.Helper()
	seg := c.run.attach()
	c.run.mu.Lock()
	waiter := c.run.pending[id]
	delete(c.run.pending, id)
	c.run.mu.Unlock()
	if waiter == nil {
		c.t.Fatalf("%s isn't waiting", id)
	}
	waiter <- mcpToolResult{Content: []map[string]any{{"type": "text", "text": "result of " + id}}}
	close(waiter)
	select {
	case <-c.called[id]:
	case <-time.After(5 * time.Second):
		c.t.Fatalf("%s's call didn't return", id)
	}
	return seg
}

// reply is what the client is told of one reply: its words, its calls'
// ids and arguments, its stop and output count.
func (c *tuiCalls) reply(seg chan Event) (text string, calls []string, stop string, out int, raw string) {
	c.t.Helper()
	done := make(chan struct{})
	rec := httptest.NewRecorder()
	go func() {
		defer close(done)
		var usage Usage
		relay(rec, httptest.NewRequest("POST", "/v1/messages", nil), provider.Anthropic, "Claude Code", &Request{Model: "claude-haiku-5-5", Stream: true}, seg, &usage,
			func() {}, nil, func(string, string, bool) {})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		c.t.Fatal("the reply never ended: the client never heard the call (deadlock)")
	}
	raw = rec.Body.String()
	for _, l := range strings.Split(raw, "\n") {
		data, found := strings.CutPrefix(l, "data: ")
		if !found {
			continue
		}
		var ev struct {
			Type         string `json:"type"`
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id"`
			} `json:"content_block"`
			Delta struct {
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			Usage struct {
				Output int `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "content_block_start":
			if ev.ContentBlock.Type == "tool_use" {
				calls = append(calls, ev.ContentBlock.ID)
			}
		case "content_block_delta":
			text += ev.Delta.Text
			if ev.Delta.PartialJSON != "" {
				calls[len(calls)-1] += ev.Delta.PartialJSON
			}
		case "message_delta":
			stop, out = ev.Delta.StopReason, ev.Usage.Output
		}
	}
	return
}

func tuiRow(id string, block string, stop string, out int) string {
	return `{"type":"assistant","message":{"id":"` + id + `","model":"claude-haiku-5-5","role":"assistant","content":[` + block + `],"stop_reason":"` + stop + `","usage":{"input_tokens":3,"output_tokens":` + itoa(out) + `}}}`
}

func tuiCall(id string, p int) string {
	return `{"type":"tool_use","id":"` + id + `","name":"mcp__magpie__Read","input":{"p":` + itoa(p) + `}}`
}

const tuiDone = `{"type":"result","subtype":"success","is_error":false,"result":"done"}`

// Claude Code may write the message calling a client's tool only once the
// call is answered, which only the client can do: the call tells the
// client itself, and the message written later isn't told again, its
// output counted in the next reply.
func TestClaudeTUICallBeforeItsMessage(t *testing.T) {
	c := newTUICalls(t)
	first := c.run.segment
	c.call("toolu_a", `{"p":1}`)
	text, calls, stop, _, raw := c.reply(first)
	if text != "" || len(calls) != 1 || calls[0] != `toolu_a{"p":1}` || stop != "tool_use" {
		t.Fatalf("first reply %q %v %q:\n%s", text, calls, stop, raw)
	}
	seg := c.answer("toolu_a")
	c.write(
		tuiRow("msg_late", `{"type":"thinking","thinking":"I'll read","signature":"sig"}`, "tool_use", 40),
		tuiRow("msg_late", tuiCall("toolu_a", 1), "tool_use", 40),
		tuiRow("msg_ans", `{"type":"text","text":"done"}`, "end_turn", 7),
		tuiDone,
	)
	text, calls, stop, out, raw := c.reply(seg)
	if text != "done" || len(calls) != 0 || stop != "end_turn" || strings.Contains(raw, "I'll read") {
		t.Fatalf("second reply %q %v %q:\n%s", text, calls, stop, raw)
	}
	if out != 47 {
		t.Fatalf("output %d, not the late message's 40 and the answer's 7:\n%s", out, raw)
	}
}

// The message written before its call is answered, as most are: the
// client is told the call once, from the transcript.
func TestClaudeTUICallAfterItsMessage(t *testing.T) {
	c := newTUICalls(t)
	first := c.run.segment
	c.write(
		tuiRow("msg_call", `{"type":"text","text":"reading"}`, "tool_use", 40),
		tuiRow("msg_call", tuiCall("toolu_a", 1), "tool_use", 40),
	)
	c.call("toolu_a", `{"p":1}`)
	text, calls, stop, _, raw := c.reply(first)
	if text != "reading" || len(calls) != 1 || calls[0] != `toolu_a{"p":1}` || stop != "tool_use" {
		t.Fatalf("first reply %q %v %q:\n%s", text, calls, stop, raw)
	}
	time.Sleep(3 * callFirst) // the call's own turn to tell the client passes
	seg := c.answer("toolu_a")
	c.write(tuiRow("msg_ans", `{"type":"text","text":"done"}`, "end_turn", 7), tuiDone)
	text, calls, stop, out, raw := c.reply(seg)
	if text != "done" || len(calls) != 0 || stop != "end_turn" || out != 7 {
		t.Fatalf("second reply %q %v %q %d:\n%s", text, calls, stop, out, raw)
	}
}

// Two calls in one message, which Claude Code makes one after the other:
// each is a reply of its own whether the message is written after both
// or between them, and the answer after it is told.
func TestClaudeTUIParallelCallsBeforeTheirMessage(t *testing.T) {
	for _, between := range []bool{false, true} {
		t.Run(map[bool]string{false: "written after both", true: "written between"}[between], func(t *testing.T) {
			c := newTUICalls(t)
			first := c.run.segment
			c.call("toolu_a", `{"p":1}`)
			if _, calls, _, _, raw := c.reply(first); len(calls) != 1 || !strings.HasPrefix(calls[0], "toolu_a") {
				t.Fatalf("first reply %v:\n%s", calls, raw)
			}
			seg := c.answer("toolu_a")
			late := []string{
				tuiRow("msg_late", tuiCall("toolu_a", 1), "tool_use", 40),
				tuiRow("msg_late", tuiCall("toolu_b", 2), "tool_use", 40),
			}
			if between {
				c.write(late...)
			}
			c.call("toolu_b", `{"p":2}`)
			if _, calls, _, _, raw := c.reply(seg); len(calls) != 1 || calls[0] != `toolu_b{"p":2}` {
				t.Fatalf("second reply %v:\n%s", calls, raw)
			}
			seg = c.answer("toolu_b")
			if !between {
				c.write(late...)
			}
			c.write(tuiRow("msg_ans", `{"type":"text","text":"done"}`, "end_turn", 7), tuiDone)
			// the late message's output is told with the reply after it
			want := 47
			if between {
				want = 7
			}
			text, calls, stop, out, raw := c.reply(seg)
			if text != "done" || len(calls) != 0 || stop != "end_turn" || out != want {
				t.Fatalf("third reply %q %v %q %d:\n%s", text, calls, stop, out, raw)
			}
		})
	}
}

// A message's second call may come in before its input is written: Claude
// Code makes the calls of one message side by side, and the input of one
// still being streamed is left out of the call it hands over (#4611). Told
// as it came, it reaches the client with no arguments and fails; held, it
// is told whole by the message written after it.
func TestClaudeTUICallWithNoInputYetWaitsForItsMessage(t *testing.T) {
	c := newTUICalls(t)
	first := c.run.segment
	c.call("toolu_a", `{"p":1}`)
	if _, calls, _, _, raw := c.reply(first); len(calls) != 1 || calls[0] != `toolu_a{"p":1}` {
		t.Fatalf("first reply %v:\n%s", calls, raw)
	}
	seg := c.answer("toolu_a")
	c.call("toolu_b", "")     // its input not written yet: no arguments at all
	time.Sleep(3 * callFirst) // the call's own turn to tell the client passes
	c.write(
		tuiRow("msg_late", tuiCall("toolu_a", 1), "tool_use", 40),
		tuiRow("msg_late", tuiCall("toolu_b", 2), "tool_use", 40),
	)
	text, calls, stop, _, raw := c.reply(seg)
	if text != "" || len(calls) != 1 || calls[0] != `toolu_b{"p":2}` || stop != "tool_use" {
		t.Fatalf("second reply %q %v %q:\n%s", text, calls, stop, raw)
	}
	c.answer("toolu_b") // the result, once the client has run it
}

// se is a stream_event line, as the helper tells what its tap read.
func se(event string) string { return `{"type":"stream_event","event":` + event + `}` }

// A streamed message's call is told once: its call, come before its
// stream ends, doesn't tell the client itself too.
func TestClaudeTUIStreamedCallToldOnce(t *testing.T) {
	c := newTUICalls(t)
	first := c.run.segment
	c.write(
		se(`{"type":"message_start","message":{"id":"msg_call","model":"claude-haiku-5-5","usage":{"input_tokens":3}}}`),
		se(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		se(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"reading"}}`),
		se(`{"type":"content_block_stop","index":0}`),
		se(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_a","name":"mcp__magpie__Read","input":{}}}`),
		se(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"p\":1}"}}`),
	)
	c.call("toolu_a", `{"p":1}`)
	time.Sleep(3 * callFirst) // the call's own turn to tell the client passes
	c.write(
		se(`{"type":"content_block_stop","index":1}`),
		se(`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":40}}`),
		se(`{"type":"message_stop"}`),
		tuiRow("msg_call", `{"type":"text","text":"reading"}`, "tool_use", 40),
		tuiRow("msg_call", tuiCall("toolu_a", 1), "tool_use", 40),
	)
	text, calls, stop, _, raw := c.reply(first)
	if text != "reading" || len(calls) != 1 || calls[0] != `toolu_a{"p":1}` || stop != "tool_use" {
		t.Fatalf("first reply %q %v %q:\n%s", text, calls, stop, raw)
	}
}

// A streamed message's whole rows can come as the next message streams,
// the transcript behind the tap: they are that message's, not one more.
func TestClaudeTUILateRowOfStreamed(t *testing.T) {
	c := newTUICalls(t)
	first := c.run.segment
	c.write(
		se(`{"type":"message_start","message":{"id":"msg_1","model":"claude-haiku-5-5","usage":{"input_tokens":3}}}`),
		se(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		se(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"a"}}`),
		se(`{"type":"content_block_stop","index":0}`),
		se(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"srvtoolu_1","name":"WebSearch","input":{}}}`),
		se(`{"type":"content_block_stop","index":1}`),
		se(`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":4}}`),
		se(`{"type":"message_stop"}`),
		se(`{"type":"message_start","message":{"id":"msg_2","model":"claude-haiku-5-5","usage":{"input_tokens":3}}}`),
		se(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		tuiRow("msg_1", `{"type":"text","text":"a"}`, "tool_use", 4),
		se(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"b"}}`),
		se(`{"type":"content_block_stop","index":0}`),
		se(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`),
		se(`{"type":"message_stop"}`),
		tuiRow("msg_2", `{"type":"text","text":"b"}`, "end_turn", 2),
		tuiDone,
	)
	text, _, stop, _, raw := c.reply(first)
	if text != "ab" || stop != "end_turn" {
		t.Fatalf("reply %q %q:\n%s", text, stop, raw)
	}
}

// Two turns in a row whose call tells the client itself, each message
// written once its call is answered: the second message is left out as
// the first was. The first message left out, still taken as the one left
// out, made the second look like a message written after a call it
// didn't have: its call was given up on, and the second message told the
// client whole, its call a second time, which the client ran again.
func TestClaudeTUICallsBeforeTheirMessagesTwoTurns(t *testing.T) {
	c := newTUICalls(t)
	first := c.run.segment
	c.call("toolu_a", `{"p":1}`)
	if _, calls, _, _, raw := c.reply(first); len(calls) != 1 || calls[0] != `toolu_a{"p":1}` {
		t.Fatalf("first reply %v:\n%s", calls, raw)
	}
	seg := c.answer("toolu_a")
	c.write(
		tuiRow("msg_late1", `{"type":"thinking","thinking":"first look","signature":"sig"}`, "tool_use", 40),
		tuiRow("msg_late1", tuiCall("toolu_a", 1), "tool_use", 40),
	)
	c.call("toolu_b", `{"p":2}`)
	if _, calls, _, _, raw := c.reply(seg); len(calls) != 1 || calls[0] != `toolu_b{"p":2}` || strings.Contains(raw, "first look") {
		t.Fatalf("second reply %v:\n%s", calls, raw)
	}
	seg = c.answer("toolu_b")
	c.write(
		tuiRow("msg_late2", `{"type":"thinking","thinking":"second look","signature":"sig"}`, "tool_use", 30),
		tuiRow("msg_late2", tuiCall("toolu_b", 2), "tool_use", 30),
		tuiRow("msg_ans", `{"type":"text","text":"done"}`, "end_turn", 7),
		tuiDone,
	)
	text, calls, stop, out, raw := c.reply(seg)
	if len(calls) != 0 || strings.Contains(raw, "second look") {
		t.Fatalf("third reply told toolu_b again %v:\n%s", calls, raw)
	}
	if text != "done" || stop != "end_turn" || out != 37 {
		t.Fatalf("third reply %q %q %d, not done with the second message's 30 and the answer's 7:\n%s", text, stop, out, raw)
	}
}

// The next turn's message written while its call waits on the client, so
// no reply is open, with a call in it not made yet. The message left out
// before it made this one look like one written after calls it didn't
// have: told whole with no reply to hear it, it marked the call not made
// yet as told, and when that call came the client never heard of it.
func TestClaudeTUIMessageWrittenWhileItsCallWaits(t *testing.T) {
	c := newTUICalls(t)
	first := c.run.segment
	c.call("toolu_a", `{"p":1}`)
	if _, calls, _, _, raw := c.reply(first); len(calls) != 1 || calls[0] != `toolu_a{"p":1}` {
		t.Fatalf("first reply %v:\n%s", calls, raw)
	}
	seg := c.answer("toolu_a")
	c.write(tuiRow("msg_late1", tuiCall("toolu_a", 1), "tool_use", 40))
	c.call("toolu_b", `{"p":2}`)
	if _, calls, _, _, raw := c.reply(seg); len(calls) != 1 || calls[0] != `toolu_b{"p":2}` {
		t.Fatalf("second reply %v:\n%s", calls, raw)
	}
	c.write(
		tuiRow("msg_late2", tuiCall("toolu_b", 2), "tool_use", 30),
		tuiRow("msg_late2", tuiCall("toolu_c", 3), "tool_use", 30),
		`{}`, // read only once the rows before it are done with
	)
	seg = c.answer("toolu_b")
	c.call("toolu_c", `{"p":3}`)
	if _, calls, _, _, raw := c.reply(seg); len(calls) != 1 || calls[0] != `toolu_c{"p":3}` {
		t.Fatalf("third reply %v:\n%s", calls, raw)
	}
	seg = c.answer("toolu_c")
	c.write(tuiRow("msg_ans", `{"type":"text","text":"done"}`, "end_turn", 7), tuiDone)
	if text, calls, stop, _, raw := c.reply(seg); text != "done" || len(calls) != 0 || stop != "end_turn" {
		t.Fatalf("last reply %q %v %q:\n%s", text, calls, stop, raw)
	}
}

// notInMessage is the log line of calls told by themselves given up on.
const notInMessage = "were not in the message written after them"

// toldOnce fails when a call is told the client in more than one reply.
func toldOnce(t *testing.T, replies [][]string) {
	t.Helper()
	seen := map[string]int{}
	for i, calls := range replies {
		for _, c := range calls {
			id, _, _ := strings.Cut(c, "{")
			if j, ok := seen[id]; ok {
				t.Fatalf("%s told in reply %d and again in reply %d: %v", id, j+1, i+1, replies)
			}
			seen[id] = i
		}
	}
}

// The shape seen in production: a message of three calls, each told by
// itself, then the next turn's message of four, its first two told by
// themselves before it is written and the other two made after it. Every
// call is told once and none is given up on.
func TestClaudeTUIProductionShape(t *testing.T) {
	logs := keepLog(t)
	c := newTUICalls(t)
	seg := c.run.segment
	var replies [][]string
	one := func(id, args string) {
		t.Helper()
		c.call(id, args)
		_, calls, _, _, raw := c.reply(seg)
		if len(calls) != 1 || calls[0] != id+args {
			t.Fatalf("reply for %s told %v:\n%s", id, calls, raw)
		}
		replies = append(replies, calls)
		seg = c.answer(id)
	}
	one("toolu_a1", `{"p":1}`)
	one("toolu_a2", `{"p":2}`)
	one("toolu_a3", `{"p":3}`)
	c.write(
		tuiRow("msg_m1", `{"type":"thinking","thinking":"look one","signature":"sig"}`, "tool_use", 50),
		tuiRow("msg_m1", tuiCall("toolu_a1", 1), "tool_use", 50),
		tuiRow("msg_m1", tuiCall("toolu_a2", 2), "tool_use", 50),
		tuiRow("msg_m1", tuiCall("toolu_a3", 3), "tool_use", 50),
		`{}`,
	)
	one("toolu_b1", `{"p":4}`)
	one("toolu_b2", `{"p":5}`)
	c.write(
		tuiRow("msg_m2", `{"type":"thinking","thinking":"look two","signature":"sig"}`, "tool_use", 60),
		tuiRow("msg_m2", tuiCall("toolu_b1", 4), "tool_use", 60),
		tuiRow("msg_m2", tuiCall("toolu_b2", 5), "tool_use", 60),
		tuiRow("msg_m2", tuiCall("toolu_b3", 6), "tool_use", 60),
		tuiRow("msg_m2", tuiCall("toolu_b4", 7), "tool_use", 60),
		`{}`,
	)
	one("toolu_b3", `{"p":6}`)
	one("toolu_b4", `{"p":7}`)
	c.write(tuiRow("msg_ans", `{"type":"text","text":"done"}`, "end_turn", 7), tuiDone)
	text, calls, stop, _, raw := c.reply(seg)
	replies = append(replies, calls)
	if text != "done" || len(calls) != 0 || stop != "end_turn" || strings.Contains(raw, "look") {
		t.Fatalf("last reply %q %v %q:\n%s", text, calls, stop, raw)
	}
	toldOnce(t, replies)
	if strings.Contains(logs.String(), notInMessage) {
		t.Fatalf("a call was given up on:\n%s", logs.String())
	}
}

// Four turns in a row, each call told by itself, its message written
// sometimes once the call is answered and sometimes while it waits on the
// client (no reply open); the third turn's message has a call not made
// yet. Every call is told once, none is given up on, and the fourth
// message's output is told with the answer.
func TestClaudeTUIFourTurns(t *testing.T) {
	logs := keepLog(t)
	c := newTUICalls(t)
	seg := c.run.segment
	var replies [][]string
	told := func(id string, p int) {
		t.Helper()
		args := `{"p":` + itoa(p) + `}`
		c.call(id, args)
		_, calls, _, _, raw := c.reply(seg)
		if len(calls) != 1 || calls[0] != id+args {
			t.Fatalf("reply for %s told %v:\n%s", id, calls, raw)
		}
		replies = append(replies, calls)
	}
	// 1: written once answered
	told("toolu_t1", 1)
	seg = c.answer("toolu_t1")
	c.write(tuiRow("msg_t1", `{"type":"thinking","thinking":"think 1","signature":"s"}`, "tool_use", 11),
		tuiRow("msg_t1", tuiCall("toolu_t1", 1), "tool_use", 11))
	// 2: written while it waits
	told("toolu_t2", 2)
	c.write(tuiRow("msg_t2", tuiCall("toolu_t2", 2), "tool_use", 12), `{}`)
	seg = c.answer("toolu_t2")
	// 3: written while it waits, with a call not made yet
	told("toolu_t3", 3)
	c.write(tuiRow("msg_t3", tuiCall("toolu_t3", 3), "tool_use", 13),
		tuiRow("msg_t3", tuiCall("toolu_t3b", 4), "tool_use", 13), `{}`)
	seg = c.answer("toolu_t3")
	told("toolu_t3b", 4)
	seg = c.answer("toolu_t3b")
	// 4: written once answered
	told("toolu_t4", 5)
	seg = c.answer("toolu_t4")
	c.write(tuiRow("msg_t4", `{"type":"thinking","thinking":"think 4","signature":"s"}`, "tool_use", 14),
		tuiRow("msg_t4", tuiCall("toolu_t4", 5), "tool_use", 14),
		tuiRow("msg_ans", `{"type":"text","text":"done"}`, "end_turn", 7), tuiDone)
	text, calls, stop, out, raw := c.reply(seg)
	replies = append(replies, calls)
	if text != "done" || len(calls) != 0 || stop != "end_turn" || strings.Contains(raw, "think") {
		t.Fatalf("last reply %q %v %q:\n%s", text, calls, stop, raw)
	}
	if out != 21 {
		t.Fatalf("output %d, not the fourth message's 14 and the answer's 7:\n%s", out, raw)
	}
	toldOnce(t, replies)
	if strings.Contains(logs.String(), notInMessage) {
		t.Fatalf("a call was given up on:\n%s", logs.String())
	}
}

// A message written after a call told by itself that really hasn't it:
// the call is given up on when the message after it comes, logged as that
// one call alone, while a call told by itself since waits for the message
// after it and isn't told again.
func TestClaudeTUIMessageReallyWithoutItsCall(t *testing.T) {
	logs := keepLog(t)
	c := newTUICalls(t)
	var replies [][]string
	first := c.run.segment
	c.call("toolu_a", `{"p":1}`)
	_, calls, _, _, raw := c.reply(first)
	if len(calls) != 1 || calls[0] != `toolu_a{"p":1}` {
		t.Fatalf("first reply %v:\n%s", calls, raw)
	}
	replies = append(replies, calls)
	seg := c.answer("toolu_a")
	// written after toolu_a, without it
	c.write(tuiRow("msg_m1", tuiCall("toolu_x", 2), "tool_use", 20), `{}`)
	c.call("toolu_x", `{"p":2}`)
	if _, calls, _, _, raw = c.reply(seg); len(calls) != 1 || calls[0] != `toolu_x{"p":2}` {
		t.Fatalf("second reply %v:\n%s", calls, raw)
	}
	replies = append(replies, calls)
	seg = c.answer("toolu_x")
	c.call("toolu_b", `{"p":3}`)
	if _, calls, _, _, raw = c.reply(seg); len(calls) != 1 || calls[0] != `toolu_b{"p":3}` {
		t.Fatalf("third reply %v:\n%s", calls, raw)
	}
	replies = append(replies, calls)
	seg = c.answer("toolu_b")
	c.write(
		tuiRow("msg_m2", `{"type":"thinking","thinking":"second look","signature":"sig"}`, "tool_use", 30),
		tuiRow("msg_m2", tuiCall("toolu_b", 3), "tool_use", 30),
		tuiRow("msg_ans", `{"type":"text","text":"done"}`, "end_turn", 7),
		tuiDone,
	)
	text, calls, stop, _, raw := c.reply(seg)
	replies = append(replies, calls)
	if text != "done" || len(calls) != 0 || stop != "end_turn" || strings.Contains(raw, "second look") {
		t.Fatalf("last reply %q %v %q:\n%s", text, calls, stop, raw)
	}
	toldOnce(t, replies)
	got := strings.Count(logs.String(), notInMessage)
	if got != 1 || !strings.Contains(logs.String(), "claude: 1 calls told by themselves "+notInMessage) {
		t.Fatalf("want one warning for 1 call, got %d:\n%s", got, logs.String())
	}
}

// The message after one that left out a call told by itself, its own
// calls not made yet and its turn not over: the call is given up on then,
// and the message is told whole, its words with its call. Kept waiting for
// a call that won't come, the message was left out in turn and its words
// were lost; only the turn's last message gave the call up.
func TestClaudeTUIMessageAfterOneWithoutItsCall(t *testing.T) {
	logs := keepLog(t)
	c := newTUICalls(t)
	var replies [][]string
	first := c.run.segment
	c.call("toolu_a", `{"p":1}`)
	if _, calls, _, _, raw := c.reply(first); len(calls) != 1 || calls[0] != `toolu_a{"p":1}` {
		t.Fatalf("first reply %v:\n%s", calls, raw)
	}
	replies = append(replies, []string{`toolu_a{"p":1}`})
	seg := c.answer("toolu_a")
	// written after toolu_a, without it
	c.write(tuiRow("msg_m1", tuiCall("toolu_x", 2), "tool_use", 20), `{}`)
	c.call("toolu_x", `{"p":2}`)
	_, calls, _, _, raw := c.reply(seg)
	if len(calls) != 1 || calls[0] != `toolu_x{"p":2}` {
		t.Fatalf("second reply %v:\n%s", calls, raw)
	}
	replies = append(replies, calls)
	seg = c.answer("toolu_x")
	c.write(
		tuiRow("msg_m2", `{"type":"text","text":"looking again"}`, "tool_use", 30),
		tuiRow("msg_m2", tuiCall("toolu_y", 3), "tool_use", 30),
	)
	c.call("toolu_y", `{"p":3}`)
	text, calls, stop, _, raw := c.reply(seg)
	if text != "looking again" || len(calls) != 1 || calls[0] != `toolu_y{"p":3}` || stop != "tool_use" {
		t.Fatalf("third reply %q %v %q:\n%s", text, calls, stop, raw)
	}
	replies = append(replies, calls)
	time.Sleep(3 * callFirst) // the call's own turn to tell the client passes
	seg = c.answer("toolu_y")
	c.write(tuiRow("msg_ans", `{"type":"text","text":"done"}`, "end_turn", 7), tuiDone)
	text, calls, stop, _, raw = c.reply(seg)
	replies = append(replies, calls)
	if text != "done" || len(calls) != 0 || stop != "end_turn" {
		t.Fatalf("last reply %q %v %q:\n%s", text, calls, stop, raw)
	}
	toldOnce(t, replies)
	got := strings.Count(logs.String(), notInMessage)
	if got != 1 || !strings.Contains(logs.String(), "claude: 1 calls told by themselves "+notInMessage) {
		t.Fatalf("want one warning for 1 call, got %d:\n%s", got, logs.String())
	}
}

// A call told by itself whose message is never written (its attempt was
// cut and asked again, say), and the model answers in words, ending the
// turn: the answer is told and the reply ends. Left out as the call's
// message, the answer was lost and nothing else ended the reply.
func TestClaudeTUILastMessageWithoutCallFirstTurn(t *testing.T) {
	c := newTUICalls(t)
	first := c.run.segment
	c.call("toolu_a", `{"p":1}`)
	if _, calls, _, _, raw := c.reply(first); len(calls) != 1 || calls[0] != `toolu_a{"p":1}` {
		t.Fatalf("first reply %v:\n%s", calls, raw)
	}
	seg := c.answer("toolu_a")
	c.write(tuiRow("msg_ans", `{"type":"text","text":"done"}`, "end_turn", 7), tuiDone)
	if text, calls, stop, _, raw := c.reply(seg); text != "done" || len(calls) != 0 || stop != "end_turn" {
		t.Fatalf("last reply %q %v %q:\n%s", text, calls, stop, raw)
	}
}

// The same after a turn whose message was left out as it should be. The
// stale lateID once told this answer by chance; giving up only on the
// calls owed left it out, and the reply never ended.
func TestClaudeTUILastMessageWithoutCallSecondTurn(t *testing.T) {
	logs := keepLog(t)
	c := newTUICalls(t)
	first := c.run.segment
	c.call("toolu_a", `{"p":1}`)
	if _, calls, _, _, raw := c.reply(first); len(calls) != 1 || calls[0] != `toolu_a{"p":1}` {
		t.Fatalf("first reply %v:\n%s", calls, raw)
	}
	seg := c.answer("toolu_a")
	c.write(tuiRow("msg_late1", tuiCall("toolu_a", 1), "tool_use", 40), `{}`)
	c.call("toolu_b", `{"p":2}`)
	if _, calls, _, _, raw := c.reply(seg); len(calls) != 1 || calls[0] != `toolu_b{"p":2}` {
		t.Fatalf("second reply %v:\n%s", calls, raw)
	}
	seg = c.answer("toolu_b")
	c.write(tuiRow("msg_ans", `{"type":"text","text":"done"}`, "end_turn", 7), tuiDone)
	text, calls, stop, _, raw := c.reply(seg)
	if text != "done" || len(calls) != 0 || stop != "end_turn" {
		t.Fatalf("last reply %q %v %q:\n%s", text, calls, stop, raw)
	}
	if !strings.Contains(logs.String(), notInMessage) {
		t.Fatalf("no warning:\n%s", logs.String())
	}
}

// The attempt that made toolu_b was asked again, and its message came with
// a call of a new id: nothing is stuck, every call is told once, and
// toolu_b is given up on, logged, once the message after that one comes.
func TestClaudeTUIRetriedMessageWithNewCall(t *testing.T) {
	logs := keepLog(t)
	c := newTUICalls(t)
	var replies [][]string
	first := c.run.segment
	c.call("toolu_a", `{"p":1}`)
	_, calls, _, _, raw := c.reply(first)
	if len(calls) != 1 || calls[0] != `toolu_a{"p":1}` {
		t.Fatalf("first reply %v:\n%s", calls, raw)
	}
	replies = append(replies, calls)
	seg := c.answer("toolu_a")
	c.write(tuiRow("msg_late1", tuiCall("toolu_a", 1), "tool_use", 40), `{}`)
	c.call("toolu_b", `{"p":2}`)
	if _, calls, _, _, raw = c.reply(seg); len(calls) != 1 || calls[0] != `toolu_b{"p":2}` {
		t.Fatalf("second reply %v:\n%s", calls, raw)
	}
	replies = append(replies, calls)
	seg = c.answer("toolu_b")
	c.write(tuiRow("msg_retry", tuiCall("toolu_c", 3), "tool_use", 20), `{}`)
	c.call("toolu_c", `{"p":3}`)
	_, calls, _, _, raw = c.reply(seg)
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "toolu_c") {
		t.Fatalf("third reply %v:\n%s", calls, raw)
	}
	replies = append(replies, calls)
	seg = c.answer("toolu_c")
	c.write(tuiRow("msg_ans", `{"type":"text","text":"done"}`, "end_turn", 7), tuiDone)
	text, calls, stop, _, raw := c.reply(seg)
	replies = append(replies, calls)
	if text != "done" || len(calls) != 0 || stop != "end_turn" {
		t.Fatalf("last reply %q %v %q:\n%s", text, calls, stop, raw)
	}
	toldOnce(t, replies)
	if !strings.Contains(logs.String(), notInMessage) {
		t.Fatalf("no warning:\n%s", logs.String())
	}
}

// Each stop that ends a turn ends the wait for a message that won't come.
func TestClaudeTUILastMessageStops(t *testing.T) {
	for _, why := range []string{"end_turn", "stop_sequence"} {
		t.Run(why, func(t *testing.T) {
			c := newTUICalls(t)
			first := c.run.segment
			c.call("toolu_a", `{"p":1}`)
			if _, calls, _, _, raw := c.reply(first); len(calls) != 1 {
				t.Fatalf("first reply %v:\n%s", calls, raw)
			}
			seg := c.answer("toolu_a")
			c.write(tuiRow("msg_ans", `{"type":"text","text":"done"}`, why, 7), tuiDone)
			if text, calls, _, _, raw := c.reply(seg); text != "done" || len(calls) != 0 {
				t.Fatalf("last reply %q %v:\n%s", text, calls, raw)
			}
		})
	}
}

// A refused message may still hold its call (seen once in real
// transcripts, its thinking row first): only a stop no call comes with
// gives up on the calls awaited, so this one is neither given up on nor
// told again.
func TestClaudeTUIRefusedMessageWithItsCall(t *testing.T) {
	logs := keepLog(t)
	c := newTUICalls(t)
	first := c.run.segment
	c.call("toolu_a", `{"p":1}`)
	if _, calls, _, _, raw := c.reply(first); len(calls) != 1 {
		t.Fatalf("first reply %v:\n%s", calls, raw)
	}
	seg := c.answer("toolu_a")
	c.write(
		tuiRow("msg_r", `{"type":"thinking","thinking":"hm","signature":"s"}`, "refusal", 9),
		tuiRow("msg_r", tuiCall("toolu_a", 1), "refusal", 9),
		tuiRow("msg_ans", `{"type":"text","text":"done"}`, "end_turn", 7),
		tuiDone,
	)
	text, calls, _, _, raw := c.reply(seg)
	if text != "done" || len(calls) != 0 || strings.Contains(raw, `"hm"`) {
		t.Fatalf("last reply %q %v:\n%s", text, calls, raw)
	}
	if strings.Contains(logs.String(), notInMessage) {
		t.Fatalf("a call was given up on:\n%s", logs.String())
	}
}
