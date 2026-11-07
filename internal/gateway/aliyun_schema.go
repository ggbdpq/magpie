package gateway

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// aliyunToolAnyRefs is body with each tool-schema $ref that reaches a
// recursive anyOf dropped where it stands — the schema holding it is left
// an empty object, which accepts exactly the values such a recursion
// describes (#5601, Eleef: Aliyun's OpenAI-compatible serving rejects the
// request outright, 400 "Invalid request parameters", one user message,
// no tool call needed; the reporter's verified control replaces the
// recursive target with {} and drops the now-unused defs). The defs only
// that ref was reading go with it, and $defs with them when it empties.
// A ref with no cycle behind it, a cycle outside an anyOf, and a chain
// that ends nowhere or never ends are left as they were, as are other
// providers, protocols and schemas.
func aliyunToolAnyRefs(p provider.Provider, to provider.Protocol, body []byte) []byte {
	if !dashScope(p) || !bytes.Contains(body, []byte(`"$ref"`)) {
		return body
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() || tools.Index <= 0 {
		return body
	}
	var cuts []span
	tools.ForEach(func(_, tool gjson.Result) bool {
		for _, at := range []string{"function.parameters", "parameters", "input_schema"} {
			if root := tool.Get(at); root.IsObject() {
				cuts = append(cuts, aliyunRecursiveRefs(body, root)...)
			}
		}
		return true
	})
	if len(cuts) == 0 {
		return body
	}
	body = cutSpans(body, cuts)
	// defs the dropped ref was the last reader of go with it, a chain of
	// them one round at a time, and $defs when it empties.
	for r := 0; r < 10; r++ {
		var gone []span
		tools = gjson.GetBytes(body, "tools")
		tools.ForEach(func(_, tool gjson.Result) bool {
			for _, at := range []string{"function.parameters", "parameters", "input_schema"} {
				if root := tool.Get(at); root.IsObject() {
					used := refNamesIn(root)
					gone = append(gone, orphanDefs(body, root, used)...)
				}
			}
			return true
		})
		if len(gone) == 0 {
			break
		}
		body = cutSpans(body, gone)
	}
	return body
}

// aliyunRecursiveRefs adds a span over each $ref under root whose target
// takes part in a reference cycle and is an anyOf: taken for any JSON
// value it stands for, an empty object says the same, and Aliyun refuses
// the recursion. The span covers the "$ref" key and its value, ready for
// cutSpans. A chain that ends nowhere or never ends adds nothing.
func aliyunRecursiveRefs(body []byte, root gjson.Result) []span {
	var cuts []span
	walkSchema(root, func(s gjson.Result) {
		ref := s.Get(`\$ref`)
		if ref.Type != gjson.String {
			return
		}
		t, ok := refTarget(root, ref.String())
		if !ok {
			return
		}
		for i := 0; ; i++ {
			r := t.Get(`\$ref`)
			if r.Type != gjson.String {
				break
			}
			n, ok := refTarget(root, r.String())
			if !ok || n.Index == t.Index {
				break // the chain ends here or circles: take t as it ends
			}
			t = n
		}
		if !t.Get("anyOf").Exists() || !reachesItself(root, t) {
			return
		}
		key := bytes.LastIndex(body[:ref.Index], []byte(`"$ref"`))
		if key < 0 {
			return
		}
		cuts = append(cuts, span{key, ref.Index + len(ref.Raw)})
	})
	return cuts
}

// reachesItself says whether any $ref under s comes back to s, directly
// or through the refs it reaches: s then stands inside a cycle.
func reachesItself(root, s gjson.Result) bool {
	seen := map[int]bool{s.Index: true}
	var walk func(n gjson.Result) bool
	walk = func(n gjson.Result) bool {
		found := false
		walkSchema(n, func(x gjson.Result) {
			if found {
				return
			}
			r := x.Get(`\$ref`)
			if r.Type != gjson.String {
				return
			}
			t, ok := refTarget(root, r.String())
			if !ok {
				return
			}
			for {
				q := t.Get(`\$ref`)
				if q.Type != gjson.String {
					break
				}
				u, ok := refTarget(root, q.String())
				if !ok || u.Index == t.Index {
					break
				}
				t = u
			}
			if t.Index == s.Index {
				found = true
			} else if !seen[t.Index] {
				seen[t.Index] = true
				found = walk(t)
			}
		})
		return found
	}
	return walk(s)
}

// refNamesIn lists the def names root's $refs read, "#/$defs/x" taking x.
func refNamesIn(root gjson.Result) map[string]bool {
	used := map[string]bool{}
	walkSchema(root, func(s gjson.Result) {
		ref := s.Get(`\$ref`)
		if ref.Type != gjson.String {
			return
		}
		if rest, ok := strings.CutPrefix(ref.String(), "#/$defs/"); ok {
			used[rest] = true
		}
	})
	return used
}

// orphanDefs adds spans over the defs under root's "$defs" nothing reads,
// and over "$defs" itself once it is empty.
func orphanDefs(body []byte, root gjson.Result, used map[string]bool) []span {
	var cuts []span
	defs := root.Get("$defs")
	if !defs.IsObject() || defs.Index <= 0 {
		return nil
	}
	live := 0
	defs.ForEach(func(name, def gjson.Result) bool {
		if used[name.String()] {
			live++
			return true
		}
		key := bytes.LastIndex(body[:def.Index], []byte(`"`+name.String()+`"`))
		if key >= 0 {
			cuts = append(cuts, span{key, def.Index + len(def.Raw)})
		}
		return true
	})
	if live == 0 {
		key := bytes.LastIndex(body[:defs.Index], []byte(`"$defs"`))
		if key >= 0 {
			cuts = append(cuts, span{key, defs.Index + len(defs.Raw)})
		}
	}
	return cuts
}

// span is a half-open byte range of body a cut removes.
type span struct {
	at, end int
}

// cutSpans is body with every span removed, rebuilt front to back (the
// withTypes pattern): a cut takes the comma before it with it, or the one
// after when there is no comma before, so what stays behind still parses.
// A span past an earlier cut's start, or off the body, is skipped.
func cutSpans(body []byte, cuts []span) []byte {
	for i := 1; i < len(cuts); i++ {
		for j := i; j > 0 && cuts[j-1].at > cuts[j].at; j-- {
			cuts[j], cuts[j-1] = cuts[j-1], cuts[j]
		}
	}
	var out bytes.Buffer
	out.Grow(len(body))
	last := 0
	for _, c := range cuts {
		if c.at < last || c.end > len(body) || c.at >= c.end {
			continue
		}
		k := c.at - 1
		for k >= last && (body[k] == ' ' || body[k] == '\t' || body[k] == '\n' || body[k] == '\r') {
			k--
		}
		if k >= last && body[k] == ',' {
			out.Write(body[last:k])
			last = c.end
			continue
		}
		out.Write(body[last:c.at])
		j := c.end
		for j < len(body) && (body[j] == ' ' || body[j] == '\t' || body[j] == '\n' || body[j] == '\r') {
			j++
		}
		if j < len(body) && body[j] == ',' {
			last = j + 1
		} else {
			last = c.end
		}
	}
	out.Write(body[last:])
	return out.Bytes()
}
