package gateway

import (
	"bytes"
	"encoding/json"
)

// usageTidy fills, in a Messages reply relayed as it came, the token counts
// Anthropic's message_start carries when the upstream's usage object names
// neither of them: some relays send "usage":{} there and count only in
// message_delta, and a strict client (Cursor Private Inference, #3380)
// turns the whole stream away unless both counts are numbers. They are
// written as 0; message_delta still tells the real ones, which a client
// reads over the start. A usage that names a count, and a message whose
// usage is absent altogether, go as they came. Only such an event is
// parsed and marshaled again; one that already has both counts as numbers,
// and every other event, goes byte for byte. A reply that came whole is
// filled the same way.
type usageTidy struct {
	sse bool
	buf []byte
}

func (t *usageTidy) write(b []byte) []byte {
	t.buf = append(t.buf, b...)
	if !t.sse {
		return nil
	}
	i := bytes.LastIndexByte(t.buf, '\n')
	if i < 0 {
		return nil
	}
	out := t.lines(t.buf[:i+1])
	t.buf = append(t.buf[:0], t.buf[i+1:]...)
	return out
}

func (t *usageTidy) flush() []byte {
	b := t.buf
	t.buf = nil
	if !t.sse {
		if out, ok := t.event(b); ok {
			return out
		}
		return b
	}
	return t.lines(b)
}

func (t *usageTidy) lines(b []byte) []byte {
	if !bytes.Contains(b, []byte(`"message_start"`)) {
		return append([]byte(nil), b...)
	}
	var out []byte
	for len(b) > 0 {
		line := b
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			line, b = b[:i+1], b[i+1:]
		} else {
			b = nil
		}
		body := bytes.TrimRight(line, "\r\n")
		data, ok := bytes.CutPrefix(body, []byte("data:"))
		if !ok || !bytes.Contains(data, []byte(`"message_start"`)) {
			out = append(out, line...)
			continue
		}
		nb, ok := t.event(data)
		if !ok {
			out = append(out, line...)
			continue
		}
		out = append(append(append(out, "data: "...), nb...), line[len(body):]...)
	}
	return out
}

// event is a message_start, or a whole message, with its token counts
// filled as 0 when its usage names neither, and whether that changed it.
func (t *usageTidy) event(data []byte) ([]byte, bool) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber() // the event's own numbers as they came
	var ev map[string]any
	if dec.Decode(&ev) != nil || ev == nil {
		return nil, false
	}
	if rest := bytes.TrimSpace(trimmed[dec.InputOffset():]); len(rest) > 0 {
		return nil, false
	}
	var usage map[string]any
	switch {
	case ev["type"] == "message_start":
		msg, _ := ev["message"].(map[string]any)
		if msg == nil {
			return nil, false
		}
		usage, _ = msg["usage"].(map[string]any)
	case !t.sse && ev["type"] == "message":
		usage, _ = ev["usage"].(map[string]any)
	default:
		return nil, false
	}
	if usage == nil || !fillUsageTokens(usage) {
		return nil, false
	}
	nb, err := marshalPlain(ev)
	if err != nil {
		return nil, false
	}
	return nb, true
}

// fillUsageTokens sets both counts to 0 when the usage object names
// neither of them, and reports whether it changed usage. A usage that
// names a count, number or otherwise, stays as it came.
func fillUsageTokens(usage map[string]any) bool {
	if _, ok := usage["input_tokens"]; ok {
		return false
	}
	if _, ok := usage["output_tokens"]; ok {
		return false
	}
	usage["input_tokens"] = 0
	usage["output_tokens"] = 0
	return true
}
