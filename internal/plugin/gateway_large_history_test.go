package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGatewayHookLargeHistoryKeepsImagesAndFiltersReasoning(t *testing.T) {
	sandbox(t)
	gatewayFixture(t, `export async function onRequest(body) {
  return {...body, input: body.input.filter(item => item.type !== "reasoning")};
}`)
	image := "data:image/png;base64," + strings.Repeat("A", 36<<20)
	body, err := json.Marshal(map[string]any{
		"model":  "gpt-example",
		"stream": true,
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": []any{
				map[string]any{"type": "input_text", "text": "Keep this image and conversation."},
				map[string]any{"type": "input_image", "image_url": image},
			}},
			map[string]any{"type": "reasoning", "id": "rs_foreign", "encrypted_content": "synthetic-foreign-seal"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	run := GatewayBegin(t.Context(), body, GatewayContext{
		Protocol: "responses", Path: "/backend-api/codex/responses", Model: "gpt-example", Agent: "codex",
	})
	if run == nil {
		t.Fatal("large history lost its accepted hook result; original foreign reasoning would be forwarded")
	}
	defer run.Close()
	var result struct {
		Input []struct {
			Type    string `json:"type"`
			Content []struct {
				Image string `json:"image_url"`
				Text  string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(run.Body, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Input) != 1 || result.Input[0].Type != "message" {
		t.Fatal("foreign reasoning was not removed")
	}
	if len(result.Input[0].Content) != 2 ||
		result.Input[0].Content[0].Text != "Keep this image and conversation." ||
		result.Input[0].Content[1].Image != image {
		t.Fatal("large image or readable message changed")
	}
}
