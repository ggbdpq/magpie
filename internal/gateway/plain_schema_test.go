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

// Antigravity takes items only on an array: a type that folds to something
// else, as ["string","array"] does to string, has to leave it behind or the
// schema reaches Gemini with an items on a string and the turn is turned
// away with "field predicate failed: $type == Type.ARRAY" (#3121, #3173).
func TestPlainSchemaDropsItemsOffAnArray(t *testing.T) {
	in := `{"type":"object","properties":{
		"disease":{"type":["string","array"],"items":{"type":"string","minLength":1}},
		"functions":{"type":"array","minItems":1,"items":{"type":["string","array"],"items":{"type":"string"}}},
		"actions":{"type":"array","items":{"oneOf":[{"type":["string","array"],"items":{"type":"string"}},{"type":"number"}]}}
	},"required":["disease"]}`
	want := `{"type":"object","properties":{
		"disease":{"type":"string"},
		"functions":{"type":"array","minItems":1,"items":{"type":"string"}},
		"actions":{"type":"array","items":{"type":"string"}}
	},"required":["disease"]}`
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

// a union folds to its first member that isn't null, on items as anywhere
// else (#3173): items under a oneOf come out as that member, typed.
func TestPlainSchemaFoldsAUnionOnItems(t *testing.T) {
	in := `{"type":"object","properties":{
		"tags":{"type":"array","items":{"oneOf":[{"type":"string"},{"type":"number"}]}},
		"actions":{"minItems":1,"type":"array","items":{"oneOf":[{"type":"object","properties":{"kind":{"type":"string"}},"required":["kind","gone"]},{"type":"object","properties":{"op":{"type":"string"}}}]}},
		"subject":{"anyOf":[{"oneOf":[{"type":"object","properties":{"path":{"type":"string"}}}]},{"type":"string"}]},
		"limbo":{"type":"array","items":{"oneOf":[]}}
	}}`
	want := `{"type":"object","properties":{
		"tags":{"type":"array","items":{"type":"string"}},
		"actions":{"type":"array","minItems":1,"items":{"type":"object","properties":{"kind":{"type":"string"}},"required":["kind"]}},
		"subject":{"type":"object","properties":{"path":{"type":"string"}}},
		"limbo":{"type":"array","items":{"type":"string"}}
	}}`
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
