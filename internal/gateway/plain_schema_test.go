package gateway

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Antigravity refuses a function declaration over any keyword outside its
// schema subset (#187: exclusiveMinimum from zcode's and dsh's tools).
func TestPlainSchemaKeepsOnlyWhatAntigravityTakes(t *testing.T) {
	in := `{"type":"object","$schema":"http://json-schema.org/draft-07/schema#","additionalProperties":false,
	"properties":{
		"limit":{"type":"integer","exclusiveMinimum":0,"maximum":100,"description":"how many"},
		"offset":{"type":"integer","exclusiveMinimum":0,"minimum":1},
		"ratio":{"type":"number","exclusiveMaximum":1},
		"url":{"type":"string","format":"uri","title":"URL","minLength":1,"pattern":"^https?://"},
		"tags":{"type":"array","items":{"type":"string","uniqueItems":true},"minItems":1,"uniqueItems":true,"contains":{"type":"string"}},
		"opts":{"type":"object","properties":{"deep":{"type":["boolean","null"],"default":false}},"minProperties":1,"dependentRequired":{"a":["b"]},"not":{"type":"null"}}
	},"required":["limit","gone"]}`
	want := `{"type":"object",
	"properties":{
		"limit":{"type":"integer","minimum":0,"maximum":100,"description":"how many"},
		"offset":{"type":"integer","minimum":1},
		"ratio":{"type":"number","maximum":1},
		"url":{"type":"string","minLength":1,"pattern":"^https?://"},
		"tags":{"type":"array","items":{"type":"string"},"minItems":1},
		"opts":{"type":"object","properties":{"deep":{"type":"boolean","nullable":true}},"minProperties":1}
	},"required":["limit"]}`
	var got, exp any
	if err := json.Unmarshal(plainSchema(json.RawMessage(in)), &got); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal([]byte(want), &exp)
	if !reflect.DeepEqual(got, exp) {
		b, _ := json.Marshal(got)
		t.Errorf("got %s", b)
	}
}

// Google refuses a whole request over an array schema that says no items,
// at any depth ("...properties[where].items.items: missing field", #2826).
// Claude Code's tools (ArtifactData's query.where) say an array of arrays
// in JSON Schema 2020-12 tuples — prefixItems, or items as an array — and
// both are outside what Antigravity's subset takes: once dropped, an array
// is left with no items, and an items has to be one object, not a tuple.
func TestPlainSchemaArrayAlwaysHasItems(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"recovers a tuple's first member", `{"type":"object","properties":{"query":{"type":"object","properties":{"where":{"type":"array","maxItems":10,
			"items":{"type":"array","prefixItems":[{"type":"string"},{"type":"string","enum":["eq","ne","in"]},{}]}}}}},"required":["query"]}`,
			`{"type":"object","properties":{"query":{"type":"object","properties":{"where":{"type":"array","maxItems":10,
			"items":{"type":"array","items":{"type":"string"}}}}}},"required":["query"]}`},
		{"an array that never said items", `{"type":"array","maxItems":3}`,
			`{"type":"array","maxItems":3,"items":{"type":"string"}}`},
		{"a tuple items collapses to one", `{"type":"array","items":[{"type":"number"},{"type":"boolean"},{}]}`,
			`{"type":"array","items":{"type":"number"}}`},
		{"a tuple of nothing gets the default", `{"type":"array","prefixItems":[{},{}]}`,
			`{"type":"array","items":{"type":"string"}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got, exp any
			if err := json.Unmarshal(plainSchema(json.RawMessage(tc.in)), &got); err != nil {
				t.Fatal(err)
			}
			json.Unmarshal([]byte(tc.want), &exp)
			if !reflect.DeepEqual(got, exp) {
				b, _ := json.Marshal(got)
				t.Errorf("got %s", b)
			}
		})
	}
}
