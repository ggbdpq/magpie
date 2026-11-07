package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAnthropicUsageTellsSearches: the searches a reply says its model ran
// with the provider's own web-search tool ride the Anthropic usage as
// server_tool_use.web_search_requests, as Anthropic says them — a gateway
// in front of magpie bills web search from that field, and every search on
// the route went unbilled while it was missing (#5448, aabbccgg: two
// server_tool_use blocks in the content, no server_tool_use in the usage).
func TestAnthropicUsageTellsSearches(t *testing.T) {
	b, _ := json.Marshal(Usage{searches: 2}.anthropic())
	var u struct {
		ServerToolUse *struct {
			WebSearchRequests int `json:"web_search_requests"`
		} `json:"server_tool_use"`
	}
	if json.Unmarshal(b, &u) != nil || u.ServerToolUse == nil || u.ServerToolUse.WebSearchRequests != 2 {
		t.Fatalf("usage %s, want server_tool_use.web_search_requests 2", b)
	}
	// none run: the field stays out, as Anthropic sends none
	b, _ = json.Marshal(Usage{}.anthropic())
	if strings.Contains(string(b), "server_tool_use") {
		t.Fatalf("usage %s, no server_tool_use when none ran", b)
	}
}
