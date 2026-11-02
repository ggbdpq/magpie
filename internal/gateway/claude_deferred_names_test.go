package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// A deferred tool of the client's own MCP server — mcp__linear__…,
// mcp__yfinance__… — reaches the run's model by the name the client says,
// in the deferred-tools reminder before a ToolSearch loads it (#4956) and
// in ToolSearch's answer after, and the model calls it as told, under the
// client's own name with no mcp__magpie__ before it. The run answered it
// itself ("No MCP server provides it", "No such tool available") and the
// client never saw the call; the model retried under the helper's full
// name (#4854, jsk644102-art: one failed call per deferred tool a
// session). The call is handed to the client under the name it called,
// when the client sent the tool or its conversation announced it; a name
// the client neither sent nor announced stays the run's own, for the run's
// own claude.ai connectors carry such names too.
func TestClaudeBridgeHandsOverAClientMCPToolByItsOwnName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	dir := t.TempDir()
	script := `#!/bin/sh
while read -r line; do
  case "$line" in
    *call-yfinance*) name=mcp__yfinance__get_stock_info ;;
    *call-linear*) name=mcp__linear__get_issue ;;
    *call-declared*) name=mcp__magpie__mcp__yfinance__get_stock_info ;;
    *call-read*) name=mcp__magpie__Read ;;
    *) name=mcp__connector__company_lookup ;;
  esac
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-sonnet-5","usage":{"input_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_'$$'","name":"'$name'"}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"q\":\"x\"}"}}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  # the run answers a call of its own and goes on in words (before the fix
  # every case but call-declared and call-read was one)
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m2","model":"claude-sonnet-5","usage":{"input_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"and that is all"}}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
done
`
	testenv.Program(t, filepath.Join(dir, "claude"), script)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	s := New()
	t.Cleanup(s.subscription.abortAll)

	// the client's tools: Read and the yfinance tool a ToolSearch loaded;
	// linear's get_issue is announced in the conversation only
	tools := `[{"name":"Read","input_schema":{"type":"object"}},{"name":"mcp__yfinance__get_stock_info","input_schema":{"type":"object"}}]`
	announced := "<system-reminder>\nThe following deferred tools are now available via ToolSearch. Their schemas are NOT loaded — calling them directly will fail with InputValidationError. Use ToolSearch with query \"select:<name>[,<name>...]\" to load tool schemas before calling them:\nmcp__linear__get_issue\n</system-reminder>\n"
	p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
	calls := func(what string) []string {
		t.Helper()
		body := `{"model":"claude-sonnet-5","max_tokens":100,"tools":` + tools + `,"messages":[{"role":"user","content":` + strconv.Quote(announced+what) + `}]}`
		rec := httptest.NewRecorder()
		var u Usage
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		if code, msg := s.serveClaudeSubscription(rec, req, provider.Anthropic, p, "claude-sonnet-5", []byte(body), &u); code != 200 {
			t.Fatalf("%s: %d %s", what, code, msg)
		}
		var res struct {
			Content []struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"content"`
		}
		json.Unmarshal(rec.Body.Bytes(), &res)
		var names []string
		for _, c := range res.Content {
			if c.Type == "tool_use" {
				names = append(names, c.Name)
			}
		}
		return names
	}
	for what, want := range map[string][]string{
		// loaded by a ToolSearch, called by the client's own name (#4854)
		"call-yfinance: the stock price": {"mcp__yfinance__get_stock_info"},
		// announced in the reminder, not loaded yet (#4956)
		"call-linear: the issue": {"mcp__linear__get_issue"},
		// the helper's own name still maps back to the declared one
		"call-declared: the stock again": {"mcp__yfinance__get_stock_info"},
		"call-read: a file":              {"Read"},
		// a name the client neither sent nor announced is the run's own
		// to answer — one of its account's claude.ai connectors — and
		// the client is shown no call of it
		"call-connector: the company": {},
	} {
		t.Run(what, func(t *testing.T) {
			if got := calls(what); len(got) != len(want) || len(want) > 0 && got[0] != want[0] {
				t.Errorf("the client is handed %v, want %v", got, want)
			}
		})
	}
}

// announcedTools collects the MCP tool names the client's conversation
// names as its own: those a system reminder announces (Claude Code's
// deferred-tools one, before a ToolSearch loads them) and those a tool's
// result loads (#2660). A name in neither — the run's own claude.ai
// connectors — is not the client's.
func TestAnnouncedTools(t *testing.T) {
	reminder := "<system-reminder>\ndeferred tools via ToolSearch:\nmcp__linear__get_issue, mcp__yfinance__get_stock_info\n</system-reminder>\n"
	got := announcedTools([]Message{
		{Role: "user", Parts: []Part{{Kind: Text, Text: "hi"}}},
		{Role: "user", Parts: []Part{{Kind: Text, Text: reminder + "and go"}}},
		{Role: "assistant", Parts: []Part{{Kind: ToolCall, Name: "ToolSearch"}}},
		{Role: "user", Parts: []Part{{Kind: ToolResult, Loads: []string{"mcp__linear__get_issue"}}}},
	})
	for _, name := range []string{"mcp__linear__get_issue", "mcp__yfinance__get_stock_info"} {
		if !got[name] {
			t.Errorf("%q not announced: %v", name, got)
		}
	}
	if got["mcp__connector__company_lookup"] || got["Read"] || got["toolu_1"] {
		t.Errorf("a name the client never said is announced: %v", got)
	}
	if len(got) != 2 {
		t.Fatalf("announced %v", got)
	}
}
