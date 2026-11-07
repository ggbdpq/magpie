package gateway

import (
	"encoding/json"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// TestAliyunToolAnyRefs: Aliyun's OpenAI-compatible serving (DashScope)
// rejects a tool schema whose $ref reaches a recursive anyOf outright,
// 400 "Invalid request parameters" (#5601, Eleef: kimi-k3.1-pro, one
// user message, no tool call), though an empty object holds the values
// the recursion describes — the reporter's verified control. The ref is
// dropped where it stands (an empty object is what remains), the defs
// only it read go with it, and $defs with them when it empties. Refs
// with no cycle behind them, cycles outside an anyOf, and other
// providers are left as they were.
func TestAliyunToolAnyRefs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	aliyun := provider.Provider{ID: "aliyun", Name: "Aliyun", Key: "k", Chat: "https://dashscope.aliyuncs.com/compatible-mode/v1", Models: []string{"kimi-k3.1-pro"}}
	other := provider.Provider{ID: "other", Name: "Other", Key: "k", Chat: "https://api.other.test/v1", Models: []string{"m"}}
	schema := `{"type":"object","properties":{"mode":{"type":"string","enum":["repositories","name"]},` +
		`"secrets":{"type":"array","items":{"type":"object","properties":{"name":{"type":"string"},` +
		`"target":{"$ref":"#/$defs/__schema0"}},"required":["name"],"additionalProperties":false}}},` +
		`"required":["mode"],"additionalProperties":false,` +
		`"$defs":{"__schema0":{"anyOf":[{"type":"string"},{"type":"number"},{"type":"boolean"},{"type":"null"},` +
		`{"type":"array","items":{"$ref":"#/$defs/__schema0"}},` +
		`{"type":"object","properties":{},"additionalProperties":{"$ref":"#/$defs/__schema0"}}]}}}`
	body := `{"tools":[{"type":"function","name":"request_environment_input","strict":false,"parameters":` + schema + `}]}`

	out := aliyunToolAnyRefs(aliyun, provider.Chat, []byte(body))
	var o map[string]any
	if err := json.Unmarshal(out, &o); err != nil {
		t.Fatalf("rewritten body is not JSON: %v: %s", err, out)
	}
	params := o["tools"].([]any)[0].(map[string]any)["parameters"].(map[string]any)
	if _, has := params["$defs"]; has {
		t.Fatalf("$defs kept: %s", out)
	}
	props := params["properties"].(map[string]any)
	target := props["secrets"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["target"].(map[string]any)
	if len(target) != 0 {
		t.Fatalf("target not emptied: %v", target)
	}
	if _, ok := props["mode"]; !ok {
		t.Fatalf("mode lost: %s", out)
	}
	if string(aliyunToolAnyRefs(other, provider.Chat, []byte(body))) != body {
		t.Fatalf("a non-Aliyun provider's body was touched: %s", out)
	}

	simple := `{"tools":[{"type":"function","name":"t","parameters":{"type":"object","properties":{"p":{"$ref":"#/$defs/P"}},"$defs":{"P":{"type":"string"}}}}]}`
	if string(aliyunToolAnyRefs(aliyun, provider.Chat, []byte(simple))) != simple {
		t.Fatalf("a ref with no cycle behind it was touched: %s", aliyunToolAnyRefs(aliyun, provider.Chat, []byte(simple)))
	}
	oneof := `{"tools":[{"type":"function","name":"t","parameters":{"type":"object","properties":{"p":{"$ref":"#/$defs/R"}},"$defs":{"R":{"oneOf":[{"type":"string"},{"$ref":"#/$defs/R"}]}}}}]}`
	if string(aliyunToolAnyRefs(aliyun, provider.Chat, []byte(oneof))) != oneof {
		t.Fatalf("a cycle outside an anyOf was touched: %s", aliyunToolAnyRefs(aliyun, provider.Chat, []byte(oneof)))
	}
}
