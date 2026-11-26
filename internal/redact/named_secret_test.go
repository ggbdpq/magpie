package redact

import (
	"strings"
	"testing"
)

// Tool arguments whose key names a secret's path or uses bare "key" with a
// non-secret value, or whose value is a path, line ref, version, or code
// identifier, must not be masked by namedSecret or seeded into known; and
// namedSecret hits must go through apply so Allow, Actions, and Seen hold
// (#7832).
func TestNamedSecretGuardsAndApply(t *testing.T) {
	Forget()
	t.Cleanup(Forget)

	t.Run("path keys do not mask or poison known", func(t *testing.T) {
		pathBody := `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"read_config","input":{"api_key_path":"foo/bar/project-dir","private_key_file":"certs/prod/key.pem","token_dir":"work/tokens/dir","apiKeyPath":"cfg/keys/main"}}]}]}`
		if out, n := MaskJSON([]byte(pathBody), Options{Secrets: true}); n != 0 || string(out) != pathBody {
			t.Fatalf("path-named tool args masked: n=%d out=%s", n, out)
		}
		if out, n := Mask("workspace/foo/bar/project-dir/internal/handler.go", Options{Secrets: true}); n != 0 || strings.Contains(out, "{{") {
			t.Fatalf("subsequent path corrupted by known: n=%d out=%q", n, out)
		}
	})

	t.Run("value-shape and bare key guards", func(t *testing.T) {
		for _, c := range []struct{ key, val string }{
			{"api_key", "/Users/alice/.ssh/id_ed25519"},
			{"credentials", "~/.config/gcloud/adc.json"},
			{"secret", "./config/secret.yaml"},
			{"token", "L643-669."},
			{"token", "get_access_token_v2"},
			{"token", "^4.0.0-beta.1"},
			{"key", "project-dir"},
			{"key", "session_config_name"},
		} {
			body := `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"run","input":{"` + c.key + `":"` + c.val + `"}}]}]}`
			if out, n := MaskJSON([]byte(body), Options{Secrets: true}); n != 0 || string(out) != body {
				t.Errorf("%s=%q wrongly masked: n=%d out=%s", c.key, c.val, n, out)
			}
		}
	})

	t.Run("routes through apply for Allow, Warn, Block, and Seen", func(t *testing.T) {
		const (
			allowedVal = "AllowedOpaqueVal91"
			warnedVal  = "WarnedOpaqueVal82"
			blockedVal = "BlockedOpaqueVal73"
			maskedVal  = "MaskedOpaqueVal64"
		)
		toolBody := func(v string) []byte {
			return []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"deploy","input":{"api_key":"` + v + `"}}]}]}`)
		}

		var hits []Hit
		oAllow := Options{Secrets: true, Allow: []string{allowedVal}, Seen: func(h Hit) { hits = append(hits, h) }}
		if out, n := MaskJSON(toolBody(allowedVal), oAllow); n != 0 || !strings.Contains(string(out), allowedVal) || len(hits) != 0 {
			t.Fatalf("Allow bypassed in namedSecret: n=%d hits=%+v out=%s", n, hits, out)
		}
		if spans := knownSpans("later "+allowedVal, Options{Secrets: true}); len(spans) != 0 {
			t.Fatalf("allowed namedSecret poisoned known: %+v", spans)
		}

		hits = nil
		oWarn := Options{Secrets: true, Actions: map[string]string{"SECRET": Warn}, Seen: func(h Hit) { hits = append(hits, h) }}
		if out, n := MaskJSON(toolBody(warnedVal), oWarn); n != 0 || !strings.Contains(string(out), warnedVal) || len(hits) != 1 || hits[0].Action != Warn {
			t.Fatalf("Warn bypassed in namedSecret: n=%d hits=%+v out=%s", n, hits, out)
		}
		if spans := knownSpans("later "+warnedVal, Options{Secrets: true}); len(spans) != 0 {
			t.Fatalf("warned namedSecret poisoned known: %+v", spans)
		}

		hits = nil
		oBlock := Options{Secrets: true, Actions: map[string]string{"SECRET": Block}, Seen: func(h Hit) { hits = append(hits, h) }}
		if out, n := MaskJSON(toolBody(blockedVal), oBlock); n != 1 || strings.Contains(string(out), blockedVal) || len(hits) != 1 || hits[0].Action != Block {
			t.Fatalf("Block bypassed in namedSecret: n=%d hits=%+v out=%s", n, hits, out)
		}

		hits = nil
		oMask := Options{Secrets: true, Seen: func(h Hit) { hits = append(hits, h) }}
		if out, n := MaskJSON(toolBody(maskedVal), oMask); n != 1 || strings.Contains(string(out), maskedVal) || len(hits) != 1 || hits[0].By != ByRule || hits[0].Kind != "SECRET" {
			t.Fatalf("Seen missing for namedSecret: n=%d hits=%+v out=%s", n, hits, out)
		}
	})

	t.Run("positive controls still mask real secrets", func(t *testing.T) {
		const realKey = "sk-proj-Ab3dEfGh1jKl2mN4oPq5rSt6"
		shapedInPath := `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"read_config","input":{"api_key_path":"` + realKey + `"}}]}]}`
		if out, n := MaskJSON([]byte(shapedInPath), Options{Secrets: true}); n != 1 || strings.Contains(string(out), realKey) {
			t.Fatalf("shaped secret inside path-named field went out: n=%d out=%s", n, out)
		}
		bareKeySecret := `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"deploy","input":{"key":"Qm27wVb9nLx4tRk8Ps3d"}}]}]}`
		if out, n := MaskJSON([]byte(bareKeySecret), Options{Secrets: true}); n != 1 || strings.Contains(string(out), "Qm27wVb9nLx4tRk8Ps3d") {
			t.Fatalf("real secret under bare key went out: n=%d out=%s", n, out)
		}
	})
}
