package edit

import "testing"

// A numeric key under a JSON array indexes it: the element at an index the
// array has is replaced, one at its length is appended, anything past that
// is refused. The array never becomes an object and no element is dropped.
func TestJSONSetArrayIndex(t *testing.T) {
	one := "{\n  \"customModels\": [\n    {\"model\": \"mine\"}\n  ]\n}\n"
	two := "{\n  \"customModels\": [\n    {\"model\": \"mine\"},\n    {\"model\": \"old\"}\n  ]\n}\n"
	tests := []struct {
		name    string
		in      string
		path    string
		value   any
		want    string // the file after SetJSON
		wantErr bool
	}{
		{
			name:  "replace the element at its index",
			in:    two,
			path:  "customModels.1",
			value: map[string]any{"model": "new"},
			want:  "{\n  \"customModels\": [\n    {\"model\": \"mine\"},\n    {\n      \"model\": \"new\"\n    }\n  ]\n}\n",
		},
		{
			name:  "replace a member of an element",
			in:    two,
			path:  "customModels.1.model",
			value: "new",
			want:  "{\n  \"customModels\": [\n    {\"model\": \"mine\"},\n    {\"model\": \"new\"}\n  ]\n}\n",
		},
		{
			name:  "insert a member into an element",
			in:    "{\n  \"customModels\": [\n    {\n      \"name\": \"mine\"\n    }\n  ]\n}\n",
			path:  "customModels.0.model",
			value: "x",
			want:  "{\n  \"customModels\": [\n    {\n      \"model\": \"x\",\n      \"name\": \"mine\"\n    }\n  ]\n}\n",
		},
		{
			name:  "append at the length",
			in:    one,
			path:  "customModels.1",
			value: map[string]any{"model": "other"},
			want:  "{\n  \"customModels\": [\n    {\"model\": \"mine\"},\n    {\n      \"model\": \"other\"\n    }\n  ]\n}\n",
		},
		{
			name:  "append at the length through a deeper key",
			in:    one,
			path:  "customModels.1.model",
			value: "new",
			want:  "{\n  \"customModels\": [\n    {\"model\": \"mine\"},\n    {\n      \"model\": \"new\"\n    }\n  ]\n}\n",
		},
		{
			name:  "append into an empty array",
			in:    "{\n  \"customModels\": []\n}\n",
			path:  "customModels.0",
			value: map[string]any{"model": "mine"},
			want:  "{\n  \"customModels\": [\n    {\n      \"model\": \"mine\"\n    }\n  ]\n}\n",
		},
		{
			name:  "create the array when the key is missing",
			in:    "{\n  \"a\": 1\n}\n",
			path:  "customModels.0.model",
			value: "x",
			want:  "{\n  \"customModels\": [\n    {\n      \"model\": \"x\"\n    }\n  ],\n  \"a\": 1\n}\n",
		},
		{
			name:    "refuse past the length",
			in:      one,
			path:    "customModels.2",
			value:   map[string]any{"model": "other"},
			wantErr: true,
		},
		{
			name:    "refuse a non-numeric key under an array",
			in:      one,
			path:    "customModels.model",
			value:   "x",
			wantErr: true,
		},
		{
			name:    "refuse creating past the array that is missing",
			in:      "{\n  \"a\": 1\n}\n",
			path:    "customModels.1.model",
			value:   "x",
			wantErr: true,
		},
		{
			name:  "a numeric key under an object stays a key",
			in:    "{\n  \"settings\": {}\n}\n",
			path:  "settings.0.model",
			value: "x",
			want:  "{\n  \"settings\": {\n    \"0\": {\n      \"model\": \"x\"\n    }\n  }\n}\n",
		},
		{
			name:  "append into a compact file",
			in:    `{"customModels":[{"model":"mine"}]}`,
			path:  "customModels.1",
			value: map[string]any{"model": "other"},
			want:  `{"customModels":[{"model":"mine"},{"model":"other"}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := tmpFile(t, "settings.json", tt.in)
			err := SetJSON(p, KV{Path: tt.path, Value: tt.value})
			if tt.wantErr {
				if err == nil {
					t.Fatalf("SetJSON(%q): want an error, got nil; file:\n%s", tt.path, read(t, p))
				}
				if got := read(t, p); got != tt.in {
					t.Fatalf("a refused set changed the file:\n%s\nwas:\n%s", got, tt.in)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := read(t, p); got != tt.want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}
