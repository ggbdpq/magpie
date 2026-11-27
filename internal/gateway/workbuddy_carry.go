package gateway

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// WorkBuddy's signed reasoning across a tool loop (#8009). WorkBuddy's
// Chat Completions replies carry the step's reasoning metadata beside the
// reasoning text, in extra_fields (its reasoning_detail, with the content
// signatures); the next request's assistant turn is to give it back, as
// CodeBuddy's sessions keep it. A Messages, Chat or Gemini client keeps
// no such field, so each step of its tool loop reached WorkBuddy with
// none of the reasoning the calls before it were made on.
//
// So the extra_fields a reply carries ride in the step's first tool
// call's id, as Gemini's signatures do (gemini_signature.go) and Grok's
// sealed reasoning (reasoning_carry.go): they are kept in magpie's folder
// (keepSignature, by their hash) wrapped with the account and model they
// belong to, and the id carries the key, <id>__wbk16__<key>. Every client
// sends a call's id back as it was given, and on the way back the payload
// goes on the assistant turn that made the call, and the id is the call's
// own again. A payload goes back only to the account and model that gave
// it (the wrapper's scope): a step routed elsewhere goes as before. A
// payload magpie no longer has (or an id a client shortened) is no
// payload: the step goes as before.

const (
	wbMark = "__wbk"
	wbEnd  = "__"
)

// wbSeal marks a tool call Part whose Sealed is a WorkBuddy extra_fields
// wrapper (wbWrap), which buildChat writes back on the assistant turn.
const wbSeal = "workbuddy-extra-fields"

// wbWho is the account or key of p a payload is scoped to, as who() reads
// it: the provider id first, so a payload never goes to another plugin's
// provider even should an account name collide.
func wbWho(p provider.Provider) string {
	if p.Account == nil && p.Key != "" {
		return p.ID + "#" + provider.KeyID(p.Key)
	}
	if p.Account != nil && p.Account.User != "" {
		return p.ID + "@" + strings.ToLower(p.Account.User)
	}
	return p.ID
}

// wbWrap wraps a reply's extra_fields with the account and model it
// belongs to, as the store keeps it: "" when there is nothing to keep.
// The payload rides as a string, so it comes back byte for byte.
func wbWrap(extra, who, model string) string {
	if strings.TrimSpace(extra) == "" || extra == "null" {
		return ""
	}
	b, err := json.Marshal(struct{ Who, Model, Extra string }{who, model, extra})
	if err != nil {
		return ""
	}
	return string(b)
}

// wbMarkedID is a call's id carrying the key a wrapper is kept by.
func wbMarkedID(id, key string) string {
	if id == "" {
		id = "call_" + newID()
	}
	return id + wbMark + strconv.Itoa(len(key)) + wbEnd + key
}

// wbUnwrap splits an id a carry marked into the call's own id and the
// wrapper it refers to ("" when magpie no longer has it). Any other id
// comes back as it is.
func wbUnwrap(id string) (string, string) {
	i := strings.LastIndex(id, wbMark)
	if i < 1 {
		return id, ""
	}
	rest := id[i+len(wbMark):]
	d := 0
	for d < len(rest) && rest[d] >= '0' && rest[d] <= '9' {
		d++
	}
	key, ok := strings.CutPrefix(rest[d:], wbEnd)
	if !ok || d == 0 || rest[:d] != strconv.Itoa(len(key)) {
		return id, ""
	}
	return id[:i], keptSignature(key)
}

// wbReplay is the extra_fields a wrapper carries when it belongs to the
// account and model asking: "" when either isn't its own (another
// account, another model — another provider's id is in the who, so it is
// covered too). A payload goes back only to the reply that gave it.
func wbReplay(wrap, who, model string) string {
	var w struct{ Who, Model, Extra string }
	if decodeJSON([]byte(wrap), &w) != nil || w.Extra == "" {
		return ""
	}
	if w.Who != who || w.Model != model {
		return ""
	}
	return w.Extra
}

// wbCarryCapture is dec with a WorkBuddy reply's extra_fields held and
// carried in its first tool call's id. The field's shape in the raw
// stream is WorkBuddy's to change, so it is read as it comes: an object's
// arrays grow by the entries not already held (a detail streams, whole or
// an entry at a time), any other value's latest chunk is the one kept.
func wbCarryCapture(dec func(string, func(Event)) error, p provider.Provider, model string) func(string, func(Event)) error {
	if p.PluginProvider() != "workbuddy" {
		return dec
	}
	who := wbWho(p)
	var held json.RawMessage
	given := false
	return func(data string, emit func(Event)) error {
		var ch struct {
			Choices []struct {
				Delta struct {
					ExtraFields json.RawMessage `json:"extra_fields"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := decodeJSON([]byte(data), &ch); err == nil {
			for _, c := range ch.Choices {
				if len(c.Delta.ExtraFields) != 0 && !bytes.Equal(c.Delta.ExtraFields, []byte("null")) {
					held = wbMerge(held, c.Delta.ExtraFields)
				}
			}
		}
		return dec(data, func(ev Event) {
			if !given && ev.Kind == KToolStart && len(held) > 0 {
				if wrap := wbWrap(string(held), who, model); wrap != "" {
					if key := keepSignature(wrap); key != "" {
						ev.ID = wbMarkedID(ev.ID, key)
						given = true
					}
				}
			}
			emit(ev)
		})
	}
}

// wbUnsignCalls takes a carried payload off a parsed request's call ids:
// a tool call keeps its own id and holds the wrapper as its Sealed
// (wbSeal), a result answers that id. Messages are copied before they
// change.
func wbUnsignCalls(r *Request) {
	for i, m := range r.Messages {
		var parts []Part
		for j, p := range m.Parts {
			var own, wrap string
			switch p.Kind {
			case ToolCall:
				own, wrap = wbUnwrap(p.ID)
				if own == p.ID {
					continue
				}
				p.ID = own
				if wrap != "" && p.SealedBy == "" {
					p.Sealed, p.SealedBy = json.RawMessage(wrap), wbSeal
				}
			case ToolResult:
				own, _ = wbUnwrap(p.CallID)
				if own == p.CallID {
					continue
				}
				p.CallID = own
			default:
				continue
			}
			if parts == nil {
				parts = slices.Clone(m.Parts)
			}
			parts[j] = p
		}
		if parts != nil {
			r.Messages[i].Parts = parts
		}
	}
}

// wbCarried is the wrapper a step's carried call holds, "" when none.
func wbCarried(parts []Part) string {
	for _, p := range parts {
		if p.Kind == ToolCall && p.SealedBy == wbSeal {
			return string(p.Sealed)
		}
	}
	return ""
}

// wbMerge joins a reply's extra_fields chunks.
func wbMerge(a, b json.RawMessage) json.RawMessage {
	if wbBlank(a) {
		return b
	}
	if wbBlank(b) {
		return a
	}
	var am, bm map[string]json.RawMessage
	if decodeJSON(a, &am) != nil || decodeJSON(b, &bm) != nil {
		return b
	}
	out := make(map[string]json.RawMessage, len(am)+len(bm))
	for k, v := range am {
		out[k] = v
	}
	for k, v := range bm {
		var into, from []json.RawMessage
		if av, ok := out[k]; ok && decodeJSON(av, &into) == nil && decodeJSON(v, &from) == nil {
			out[k] = wbJoinNew(into, from)
			continue
		}
		out[k] = v
	}
	j, err := json.Marshal(out)
	if err != nil {
		return b
	}
	return j
}

// wbJoinNew is a with b's entries appended that a doesn't hold already: a
// detail that streams whole is one list however many chunks carried it.
func wbJoinNew(a, b []json.RawMessage) json.RawMessage {
	out := a
	for _, e := range b {
		if !slices.ContainsFunc(out, func(x json.RawMessage) bool { return wbSame(x, e) }) {
			out = append(out, e)
		}
	}
	j, err := json.Marshal(out)
	if err != nil {
		return json.RawMessage("[]")
	}
	return j
}

// wbSame says two JSON values are the same once compacted: an entry's key
// order is one upstream's to keep steady (a reordered entry would come
// twice, which the upstream reading the list sees as two entries).
func wbSame(a, b json.RawMessage) bool {
	var ca, cb bytes.Buffer
	if json.Compact(&ca, a) != nil || json.Compact(&cb, b) != nil {
		return false
	}
	return ca.String() == cb.String()
}

func wbBlank(b json.RawMessage) bool {
	return len(bytes.TrimSpace(b)) == 0 || bytes.Equal(bytes.TrimSpace(b), []byte("null"))
}
