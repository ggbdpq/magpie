package gateway

import "encoding/json"

// A tool_use of the conversation's history carries the caller the upstream
// gave its call, and the client marks the blocks of the history it keeps
// for its prompt cache — the caller's call included, Claude Code's marks
// landing where the last block of a turn stands. A gateway in front of a
// Messages upstream that reads the request's cache controls before dispatch
// turns the whole request away over that pair, and every retry of the turn
// fails the same way: "Gateway kernel rejected the request cache controls
// before dispatch (CRAG_CACHE_CONTROL)", 502 after 7 tries, Claude Code
// stuck (chiyan171, #6914). Claude Desktop, which marks the same way but
// echoes no caller, works, and so does a request whose marked calls came
// without one: the pair alone is the refusal.
//
// withoutCallerMarks is a Messages request relayed as the client sent it
// (passthrough, count_tokens) with the cache marks of its caller's calls
// taken off: what the reporter asked for, the mark gone from the blocks
// that carry a caller and every other mark kept, so the cache boundary
// moves back a block and nothing is cached no more. A body without the
// pair comes back byte for byte, and a mended one keeps every block it
// didn't have to change as it came, since a request's bytes are its prompt
// cache and its client's.
func withoutCallerMarks(body []byte) []byte {
	if !bodyHas(body, `"caller"`) {
		return body
	}
	msgs, found, ok := topMember(body, "messages")
	if !ok || !found || len(msgs) == 0 || msgs[0] != '[' {
		return body
	}
	mended, changed := unmarkedCalls(msgs)
	if !changed {
		return body
	}
	return withTopValue(body, "messages", mended)
}

// unmarkedCalls is messages with the marks of its caller's calls taken off,
// and whether anything had to be.
func unmarkedCalls(msgs []byte) ([]byte, bool) {
	var out []json.RawMessage
	changed := false
	eachElement(msgs, func(m []byte) bool {
		if len(m) == 0 || m[0] != '{' {
			out = append(out, m)
			return true
		}
		content, found := memberOf(m, "content")
		if !found || len(content) == 0 || content[0] != '[' {
			out = append(out, m)
			return true
		}
		blocks, touched := unmarkedBlocks(content)
		if !touched {
			out = append(out, m)
			return true
		}
		changed = true
		out = append(out, withTopValue(m, "content", blocks))
		return true
	})
	if !changed {
		return nil, false
	}
	return rawList(out), true
}

// unmarkedBlocks is a message's content with the marks of its caller's
// calls taken off, and whether any was.
func unmarkedBlocks(content []byte) ([]byte, bool) {
	var out []json.RawMessage
	touched := false
	eachElement(content, func(b []byte) bool {
		if markedCallerCall(b) {
			out = append(out, withoutTopMember(b, "cache_control"))
			touched = true
			return true
		}
		out = append(out, b)
		return true
	})
	if !touched {
		return nil, false
	}
	return rawList(out), true
}

// markedCallerCall is whether b, a content block as the client sent it, is
// a tool_use that carries a caller and a cache_control together.
func markedCallerCall(b []byte) bool {
	if len(b) == 0 || b[0] != '{' {
		return false
	}
	typ, caller, mark := "", false, false
	eachMember(b, func(k, v []byte) bool {
		switch {
		case keyIs(k, "type"):
			if s, ok := stringBytes(v); ok {
				typ = string(s)
			}
		case keyIs(k, "caller"):
			caller = true
		case keyIs(k, "cache_control"):
			mark = true
		}
		return true
	})
	return typ == "tool_use" && caller && mark
}
