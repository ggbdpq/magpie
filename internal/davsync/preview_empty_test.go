package davsync

import (
	"context"
	"encoding/json"
	"testing"
)

// #7078 (Caho1): after a successful sync the unchanged-file shortcut
// leaves no part decisions. Both preview entry points must still encode
// parts as an array, which the Settings preview filters.
func TestPreviewUnchangedPartsArray(t *testing.T) {
	fake, cfg := scopeServer(t)
	newComputer(t).use(t)
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	syncOK(t)
	puts := fake.puts
	token := ""
	for _, tc := range []struct {
		name    string
		preview func() (Plan, error)
	}{
		{"saved", func() (Plan, error) { return Preview(context.Background()) }},
		{"form", func() (Plan, error) { return PreviewWith(context.Background(), cfg) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := tc.preview()
			if err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Parts json.RawMessage `json:"parts"`
			}
			if err := json.Unmarshal(b, &wire); err != nil {
				t.Fatal(err)
			}
			if string(wire.Parts) != "[]" {
				t.Fatalf("unchanged preview parts = %s, want []; response: %s", wire.Parts, b)
			}
			if p.Token == "" {
				t.Fatal("preview has no token")
			}
			if token != "" && token != p.Token {
				t.Fatalf("equivalent preview tokens differ: %s != %s", token, p.Token)
			}
			token = p.Token
		})
	}
	if fake.puts != puts {
		t.Fatalf("preview wrote the server: %d puts", fake.puts-puts)
	}
	if !t.Failed() {
		if err := SyncPreviewed(context.Background(), token); err != nil {
			t.Fatal(err)
		}
		if fake.puts != puts {
			t.Fatalf("unchanged previewed sync wrote the server: %d puts", fake.puts-puts)
		}
	}
}
