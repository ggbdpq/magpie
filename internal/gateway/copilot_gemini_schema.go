package gateway

import (
	"bytes"
	"slices"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// A Copilot Gemini model is served on chat completions alone, so Codex's
// tools go to it through the translation, schemas as the client declared
// them — and Copilot turns every tool-bearing turn of Codex Desktop's away
// 400 "invalid request body" on them (#4374, as #3931's comparisons
// showed: the same request with the unions as oneOf came back 200, and
// nothing else about the tools — descriptions, strict, required — made the
// difference). What it rejects is a tool schema's anyOf of object branches,
// so one whose branches are provably disjoint goes as oneOf, which says
// the same to a model choosing what to call: at most one branch can match,
// whichever says it.
//
// Disjoint is proved as #3931's fix did, and only so: two branches are
// apart when one requires a field of a string whose values (enum, const)
// the other's required field of that name never takes, or when one
// requires a field the other forbids through additionalProperties: false
// (with no patternProperties beside it, which would admit the name). A
// union that can't be proved — nullable ones among them — stays an anyOf.
//
// The refusal is not the object unions' alone: Copilot's Gemini turns a
// scalar union away the same way (#5952, qinezh — an anyOf of two
// overlapping string branches, and one of two nested integer intervals,
// each 400 "invalid request body" where the union's widest branch alone
// came back 200). An overlapping union must not go as oneOf, which says at
// most one branch, so a scalar union goes as the one branch every other
// branch provably admits nothing beside: the pure type takes every value
// the enum'd branch of its type does, and the wider of two nested numeric
// intervals takes the narrower. A union no branch takes the rest of — a
// gap between two intervals, two patterns — stays an anyOf, refusal and
// all, rather than say something the schema doesn't.
func copilotGeminiToolUnions(p provider.Provider, to provider.Protocol, body []byte) []byte {
	if to != provider.Chat || p.Account == nil || p.Account.Agent != "copilot" ||
		!geminiNamed(gjson.GetBytes(body, "model").String()) {
		return body
	}
	if !bytes.Contains(body, []byte(`"anyOf"`)) {
		return body
	}
	var at []int
	var spans []copilotUnionSpan
	gjson.GetBytes(body, "tools").ForEach(func(_, tool gjson.Result) bool {
		for _, path := range []string{"function.parameters", "parameters", "input_schema"} {
			collectCopilotUnions(body, tool.Get(path), &at, &spans)
		}
		return true
	})
	if len(at) == 0 && len(spans) == 0 {
		return body
	}
	// renamed in place, the same length, so nothing else of the body moves
	// and a cache keeps its prefix
	out := bytes.Clone(body)
	for _, i := range at {
		copy(out[i:], `"oneOf"`)
	}
	// a collapse moves what follows it, so the spans go in from the last
	// backward, each at its place in the body the renames left (the same
	// length it came in)
	slices.SortFunc(spans, func(a, b copilotUnionSpan) int { return b.at - a.at })
	for _, s := range spans {
		if s.at < 0 || s.at+len(s.raw) > len(out) || string(out[s.at:s.at+len(s.raw)]) != s.raw {
			continue
		}
		out = append(out[:s.at], append([]byte(s.goes), out[s.at+len(s.raw):]...)...)
	}
	return out
}

// copilotUnionSpan is a tool schema's anyOf node replaced whole by the one
// branch that takes the others' values: where the node opens in the body,
// the node as it came, and the branch as the client wrote it.
type copilotUnionSpan struct {
	at   int
	raw  string
	goes string
}

// collectCopilotUnions adds where each anyOf of provably disjoint object
// branches under node, a tool's schema, starts in body: a key, not a
// property of that name. It adds, beside each anyOf a branch takes the
// rest of, the span of the node that holds it — which must hold the union
// alone, a description beside it going nowhere.
func collectCopilotUnions(body []byte, node gjson.Result, at *[]int, spans *[]copilotUnionSpan) {
	switch {
	case node.IsArray():
		node.ForEach(func(_, v gjson.Result) bool {
			collectCopilotUnions(body, v, at, spans)
			return true
		})
	case node.IsObject():
		if union := node.Get("anyOf"); union.Exists() && !node.Get("oneOf").Exists() {
			if disjointObjectBranches(union) {
				if i := keyBefore(body, union.Index, `"anyOf"`); i >= 0 {
					*at = append(*at, i)
				}
			} else if branch := collapsedScalarBranch(union); branch.Raw != "" && node.Index > 0 &&
				node.Index < len(body) && body[node.Index] == '{' && nodeKeys(node) == 1 {
				*spans = append(*spans, copilotUnionSpan{at: node.Index, raw: node.Raw, goes: branch.Raw})
			}
		}
		node.ForEach(func(key, v gjson.Result) bool {
			switch key.String() {
			case "properties", "$defs", "definitions", "dependentSchemas", "patternProperties":
				v.ForEach(func(_, s gjson.Result) bool {
					collectCopilotUnions(body, s, at, spans)
					return true
				})
			case "items", "additionalProperties", "contains", "propertyNames", "not", "if", "then", "else", "allOf", "anyOf", "oneOf", "prefixItems", "unevaluatedProperties", "unevaluatedItems", "contentSchema":
				collectCopilotUnions(body, v, at, spans)
			}
			return true
		})
	}
}

// nodeKeys counts the keys node, an object, has.
func nodeKeys(node gjson.Result) int {
	n := 0
	node.ForEach(func(_, _ gjson.Result) bool { n++; return true })
	return n
}

// collapsedScalarBranch is the branch of union, an anyOf, that takes every
// value the other branches do — the one the union says the same thing as —
// or none: a scalar union of one type whose branches but for descriptions
// say only what they take, where one branch's bounds take the rest's.
// Object branches have their own proof, the disjoint one, and a branch
// that carries a $ref, a not, a nested allOf or anything else that can't
// be read whole proves nothing.
func collapsedScalarBranch(union gjson.Result) gjson.Result {
	if !union.IsArray() {
		return gjson.Result{}
	}
	branches := union.Array()
	if len(branches) < 2 {
		return gjson.Result{}
	}
	for _, b := range branches {
		if !scalarBranchReadable(b) {
			return gjson.Result{}
		}
	}
	for _, b := range branches {
		takes := true
		for _, a := range branches {
			if a.Index != b.Index && !scalarBranchImplied(a, b) {
				takes = false
				break
			}
		}
		if takes {
			return b
		}
	}
	return gjson.Result{}
}

// scalarBranchReadable is whether branch is a schema of one scalar type
// whose remaining keys say only what it takes: no $ref, no not, no
// combination, no shape of an object or an array.
func scalarBranchReadable(branch gjson.Result) bool {
	if !branch.IsObject() {
		return false
	}
	switch t := branch.Get("type").String(); t {
	case "string", "number", "integer", "boolean":
	default:
		return false
	}
	ok := true
	branch.ForEach(func(k, _ gjson.Result) bool {
		switch k.String() {
		case "type", "enum", "const", "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum",
			"minLength", "maxLength", "pattern", "format", "multipleOf", "description":
		default:
			ok = false
		}
		return ok
	})
	return ok
}

// scalarBranchImplied is whether b, a scalar branch, takes every value a
// does: outright when b is its type alone, and between two branches that
// say only numeric bounds, when each bound b says takes what a says and b
// says no multipleOf a doesn't. A bound b says where a says none keeps a's
// values out, so b takes nothing there.
func scalarBranchImplied(a, b gjson.Result) bool {
	if bPure := pureScalarBranch(b); bPure {
		return true
	}
	t := b.Get("type").String()
	if t != "number" && t != "integer" {
		return false
	}
	for _, k := range []string{"enum", "const", "minLength", "maxLength", "pattern", "format"} {
		if a.Get(k).Exists() || b.Get(k).Exists() {
			return false
		}
	}
	for _, k := range []string{"minimum", "exclusiveMinimum"} {
		av, bv := a.Get(k), b.Get(k)
		switch {
		case av.Exists() && bv.Exists():
			if bv.Float() > av.Float() {
				return false
			}
		case bv.Exists():
			return false
		}
	}
	for _, k := range []string{"maximum", "exclusiveMaximum"} {
		av, bv := a.Get(k), b.Get(k)
		switch {
		case av.Exists() && bv.Exists():
			if bv.Float() < av.Float() {
				return false
			}
		case bv.Exists():
			return false
		}
	}
	if bv := b.Get("multipleOf"); bv.Exists() {
		av := a.Get("multipleOf")
		if !av.Exists() || bv.Float() != av.Float() {
			return false
		}
	}
	return true
}

// pureScalarBranch is whether branch says its type and nothing else that
// binds: the type takes every value of its own, whatever the branch beside
// it in the union asks for.
func pureScalarBranch(branch gjson.Result) bool {
	ok := true
	branch.ForEach(func(k, _ gjson.Result) bool {
		switch k.String() {
		case "type", "description":
		default:
			ok = false
		}
		return ok
	})
	return ok
}

// disjointObjectBranches is whether union, an anyOf, is of object branches
// only and every two of them are proved apart.
func disjointObjectBranches(union gjson.Result) bool {
	if !union.IsArray() {
		return false
	}
	branches := union.Array()
	if len(branches) < 2 {
		return false
	}
	for i, a := range branches {
		if a.Get("type").String() != "object" || a.Get(`\$ref`).Exists() {
			return false
		}
		for _, b := range branches[:i] {
			if !objectBranchesApart(a, b) && !objectBranchesApart(b, a) {
				return false
			}
		}
	}
	return true
}

// objectBranchesApart is whether a is proved apart from b: a required
// field of a that b never takes. Either b forbids the field outright
// (additionalProperties: false, no patternProperties to admit it), or b
// requires it too and the string values (enum, const) the two say for it
// never meet — a field b holds optional is one b may leave out, which is
// every value of a's.
func objectBranchesApart(a, b gjson.Result) bool {
	bProps := b.Get("properties").Map()
	for _, name := range a.Get("required").Array() {
		if name.Type != gjson.String {
			continue
		}
		field := name.String()
		bProp, there := bProps[field]
		if !there {
			if b.Get("additionalProperties").Type == gjson.False && !b.Get("patternProperties").Exists() {
				return true
			}
			continue
		}
		required := false
		for _, other := range b.Get("required").Array() {
			required = required || other.Type == gjson.String && other.String() == field
		}
		av, bv := schemaStringValues(a.Get("properties").Map()[field]), schemaStringValues(bProp)
		if !required || len(av) == 0 || len(bv) == 0 {
			continue
		}
		shared := false
		for v := range av {
			shared = shared || bv[v]
		}
		if !shared {
			return true
		}
	}
	return false
}

// schemaStringValues is the string values (enum, const) of schema, nil
// when they can't be read whole (a $ref, a value that isn't a string).
func schemaStringValues(schema gjson.Result) map[string]bool {
	if schema.Get(`\$ref`).Exists() {
		return nil
	}
	values := schema.Get("enum").Array()
	if c := schema.Get("const"); c.Type == gjson.String {
		values = []gjson.Result{c}
	}
	out := map[string]bool{}
	for _, v := range values {
		if v.Type != gjson.String {
			return nil
		}
		out[v.String()] = true
	}
	return out
}
