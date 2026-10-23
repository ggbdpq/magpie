package gateway

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// strictStartUsage reads a relayed stream's message_start the way a strict
// client does (Cursor Private Inference, #3380): Anthropic's message_start
// carries a usage with input_tokens and output_tokens, and a client that
// validates the event's schema turns the whole stream away unless both are
// JSON numbers.
func strictStartUsage(t *testing.T, body string) gjson.Result {
	t.Helper()
	var raw string
	readSSE(strings.NewReader(body), func(ev, data string) error {
		if ev == "message_start" || gjson.Get(data, "type").String() == "message_start" {
			raw = data
		}
		return nil
	})
	if raw == "" {
		t.Fatalf("no message_start in reply: %s", body)
	}
	return gjson.Get(raw, "message.usage")
}

// An Anthropic-compatible relay that counts only at its end sends
// message_start with "usage":{} and the real counts in message_delta. A
// client that requires both counts on message_start turns the whole stream
// away (#3380), so the passthrough writes the missing counts as 0 and
// leaves every other event, message_delta with the real counts included,
// as it came.
func TestPassthroughFillsMessageStartUsageLeftEmpty(t *testing.T) {
	fresh(t)
	delta := "event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":13,"output_tokens":4}}` + "\n\n"
	stop := "event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"
	reply := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"usage":{}}}` + "\n\n" +
		anthropicText + delta + stop
	f := &fake{reply: reply}
	setup(t, provider.Anthropic, f)
	code, body := postMessages(t, `{"model":"fake/m1","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status = %d, want 200; body: %s", code, body)
	}
	usage := strictStartUsage(t, body)
	if usage.Get("input_tokens").Type != gjson.Number || usage.Get("output_tokens").Type != gjson.Number {
		t.Fatalf("message_start.usage counts are not numbers: %s", usage.Raw)
	}
	if usage.Get("input_tokens").Num != 0 || usage.Get("output_tokens").Num != 0 {
		t.Fatalf("filled counts = %s, want 0 and 0", usage.Raw)
	}
	if !strings.Contains(body, anthropicText) || !strings.Contains(body, delta) || !strings.Contains(body, stop) {
		t.Fatalf("events after message_start were rewritten: %s", body)
	}
}

// A stream whose message_start already has both counts is relayed byte for
// byte.
func TestPassthroughKeepsAConformingMessageStartByteForByte(t *testing.T) {
	fresh(t)
	reply := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":13,"output_tokens":1,"cache_read_input_tokens":3}}}` + "\n\n" +
		anthropicText + anthropicStop
	f := &fake{reply: reply}
	setup(t, provider.Anthropic, f)
	code, body := postMessages(t, `{"model":"fake/m1","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || body != reply {
		t.Fatalf("a conforming stream was changed: %d\n got %q\nwant %q", code, body, reply)
	}
}

// A usage that names one of the two counts goes as it came: a partial
// count on message_start is a shape relays send and the repo relays byte
// for byte, whoever reads it.
func TestPassthroughKeepsAPartialMessageStartUsageAsItCame(t *testing.T) {
	fresh(t)
	delta := "event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}` + "\n\n"
	start := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","model":"m","content":[],"usage":{"cache_read_input_tokens":9,"input_tokens":7}}}` + "\n\n"
	reply := start + anthropicText + delta + "event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"
	f := &fake{reply: reply}
	setup(t, provider.Anthropic, f)
	code, body := postMessages(t, `{"model":"fake/m1","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || body != reply {
		t.Fatalf("a partial usage was changed: %d\n got %q\nwant %q", code, body, reply)
	}
}

// A message_start whose message carries no usage at all goes as it came:
// native passthrough stays byte-preserving, and only a usage object short
// of its counts is filled.
func TestPassthroughKeepsAMessageStartWithoutUsageAsItCame(t *testing.T) {
	fresh(t)
	reply := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[]}}` + "\n\n" +
		anthropicText + anthropicStop
	f := &fake{reply: reply}
	setup(t, provider.Anthropic, f)
	code, body := postMessages(t, `{"model":"fake/m1","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || body != reply {
		t.Fatalf("a message_start without usage was changed: %d\n got %q\nwant %q", code, body, reply)
	}
}

// A whole Messages reply relayed as it came is filled the same way; one
// that already has both counts is not touched.
func TestPassthroughFillsAWholeMessagesEmptyUsage(t *testing.T) {
	fresh(t)
	f := &fake{ctype: "application/json", reply: `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"usage":{}}`}
	setup(t, provider.Anthropic, f)
	code, body := post(t, "/v1/messages", `{"model":"fake/m1","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status = %d; body: %s", code, body)
	}
	if gjson.Get(body, "usage.input_tokens").Type != gjson.Number || gjson.Get(body, "usage.output_tokens").Type != gjson.Number {
		t.Fatalf("whole usage not filled: %s", body)
	}
	if gjson.Get(body, "id").String() != "msg_1" || gjson.Get(body, "content").Raw != "[]" {
		t.Fatalf("whole message was damaged: %s", body)
	}

	f.reply = `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"usage":{"output_tokens":2,"input_tokens":5}}`
	code, body = post(t, "/v1/messages", `{"model":"fake/m1","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || body != f.reply {
		t.Fatalf("a conforming whole message was changed: %d\n got %q\nwant %q", code, body, f.reply)
	}
}

// TestUsageTidyUnit feeds the tidy a byte at a time, as a stream arrives.
func TestUsageTidyUnit(t *testing.T) {
	feed := func(sse bool, in string) string {
		t.Helper()
		u := &usageTidy{sse: sse}
		var out []byte
		for i := 0; i < len(in); i++ {
			out = append(out, u.write([]byte{in[i]})...)
		}
		return string(append(out, u.flush()...))
	}
	t.Run("empty object", func(t *testing.T) {
		in := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"usage\":{}}}\n\n"
		got := feed(true, in)
		usage := gjson.Get(strings.TrimPrefix(strings.Split(got, "\n")[1], "data: "), "message.usage")
		if usage.Get("input_tokens").Type != gjson.Number || usage.Get("output_tokens").Type != gjson.Number {
			t.Fatalf("not filled: %s", got)
		}
	})
	t.Run("null usage stays", func(t *testing.T) {
		in := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"usage\":null}}\n"
		if got := feed(true, in); got != in {
			t.Fatalf("rewrote a null usage:\n got %q\nwant %q", got, in)
		}
	})
	t.Run("missing usage stays", func(t *testing.T) {
		in := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"m\"}}\n"
		if got := feed(true, in); got != in {
			t.Fatalf("rewrote a message_start without usage:\n got %q\nwant %q", got, in)
		}
	})
	t.Run("already complete", func(t *testing.T) {
		in := "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"output_tokens\":2,\"input_tokens\":1}}}\n"
		if got := feed(true, in); got != in {
			t.Fatalf("rewrote conforming event:\n got %q\nwant %q", got, in)
		}
	})
	t.Run("cache counts only", func(t *testing.T) {
		got := feed(true, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"cache_read_input_tokens\":9}}}\n")
		usage := gjson.Get(strings.TrimPrefix(strings.TrimSpace(got), "data: "), "message.usage")
		if usage.Get("input_tokens").Type != gjson.Number || usage.Get("output_tokens").Type != gjson.Number || usage.Get("cache_read_input_tokens").Num != 9 {
			t.Fatalf("not filled: %s", got)
		}
	})
	t.Run("string count stays", func(t *testing.T) {
		in := "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":\"13\"}}}\n"
		if got := feed(true, in); got != in {
			t.Fatalf("rewrote a usage naming a count:\n got %q\nwant %q", got, in)
		}
	})
	t.Run("message delta only output", func(t *testing.T) {
		in := "data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":2}}\n"
		if got := feed(true, in); got != in {
			t.Fatalf("rewrote message_delta:\n got %q\nwant %q", got, in)
		}
	})
	t.Run("other events", func(t *testing.T) {
		in := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n"
		if got := feed(true, in); got != in {
			t.Fatalf("rewrote another event:\n got %q\nwant %q", got, in)
		}
	})
	t.Run("cut mid line", func(t *testing.T) {
		in := "data: {\"type\":\"message_start\",\"message\":{\"usage\":{"
		if got := feed(true, in); got != in {
			t.Fatalf("rewrote a cut line:\n got %q\nwant %q", got, in)
		}
	})
	t.Run("whole message", func(t *testing.T) {
		got := feed(false, `{"type":"message","id":"m","role":"assistant","content":[],"usage":{}}`)
		if gjson.Get(got, "usage.input_tokens").Type != gjson.Number || gjson.Get(got, "usage.output_tokens").Type != gjson.Number {
			t.Fatalf("whole reply not filled: %s", got)
		}
		in := `{"type":"message","usage":{"input_tokens":1,"output_tokens":2}}`
		if got := feed(false, in); got != in {
			t.Fatalf("rewrote a conforming whole message:\n got %q\nwant %q", got, in)
		}
	})
	t.Run("whole message of another shape", func(t *testing.T) {
		in := `{"choices":[]}`
		if got := feed(false, in); got != in {
			t.Fatalf("rewrote a body of another shape:\n got %q\nwant %q", got, in)
		}
	})
}
