package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/library"
)

// magpie library mcp router on|off (#2679): on, the agent is given magpie's
// one MCP server in place of the library's; off, each server's own again.
// Any other word is refused, and writes nothing. "hub", its name before,
// still works.
// A remote agent that connected once stays in library.json forever, with
// every server it was first given still ticked for it (#8063). The CLI must
// give the user a way to forget one and take its ticks back.
func TestLibraryMCPRemoteRm(t *testing.T) {
	h := t.TempDir()
	for k, v := range map[string]string{"HOME": h, "USERPROFILE": h, "XDG_CONFIG_HOME": filepath.Join(h, ".config"), "CLAUDE_CONFIG_DIR": "", "CODEX_HOME": "", "APPDATA": "", "LOCALAPPDATA": ""} {
		t.Setenv(k, v)
	}
	if err := os.MkdirAll(filepath.Join(h, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h, ".claude/settings.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(filepath.Dir(library.Dir()), "library.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"instructions":{},"mcp":[{"name":"fake","transport":"stdio","command":"x","agents":["claude","dsh"]}],"skills":[],"mcpHub":true,"remoteAgents":["dsh","other"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := libraryCmd([]string{"library", "mcp", "remote", "rm", "dsh"}); err != nil {
		t.Fatalf("#8063 removing the remote agent: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		RemoteAgents []string `json:"remoteAgents"`
		MCP          []struct {
			Name   string   `json:"name"`
			Agents []string `json:"agents"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.RemoteAgents, []string{"other"}) {
		t.Errorf("#8063 remote agents after removal: %v, want [other]", got.RemoteAgents)
	}
	if len(got.MCP) != 1 || !slices.Equal(got.MCP[0].Agents, []string{"claude"}) {
		t.Errorf("#8063 server agents after removal: %+v, want only claude", got.MCP)
	}
}

func TestLibraryMCPHub(t *testing.T) {
	h := t.TempDir()
	for k, v := range map[string]string{"HOME": h, "USERPROFILE": h, "XDG_CONFIG_HOME": filepath.Join(h, ".config"), "CLAUDE_CONFIG_DIR": "", "CODEX_HOME": "", "APPDATA": "", "LOCALAPPDATA": ""} {
		t.Setenv(k, v)
	}
	if err := os.MkdirAll(filepath.Join(h, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h, ".claude/settings.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	names := func() []string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(h, ".claude.json"))
		if err != nil {
			t.Fatal(err)
		}
		var c struct {
			MCPServers map[string]map[string]any `json:"mcpServers"`
		}
		if err := json.Unmarshal(b, &c); err != nil {
			t.Fatal(err)
		}
		var out []string
		for k := range c.MCPServers {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}
	if err := libraryCmd([]string{"library", "mcp", "add", "fs", "npx", "-y", "fs", "agents=claude"}); err != nil {
		t.Fatal(err)
	}
	if got := names(); !slices.Equal(got, []string{"fs"}) {
		t.Fatalf("before: %v", got)
	}
	if err := libraryCmd([]string{"library", "mcp", "router", "on"}); err != nil {
		t.Fatal(err)
	}
	if got := names(); !slices.Equal(got, []string{"magpie"}) {
		t.Fatalf("router on: %v", got)
	}
	if err := libraryCmd([]string{"library", "mcp", "router", "yes"}); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("router yes: %v", err)
	}
	if got := names(); !slices.Equal(got, []string{"magpie"}) {
		t.Fatalf("after a word it doesn't know: %v", got)
	}
	// a server kept direct (#4382) is its own entry again, beside no hub
	// entry once no server is behind it; shared, behind the router again
	// (said with "hub", its name before)
	if err := libraryCmd([]string{"library", "mcp", "router", "fs", "direct"}); err != nil {
		t.Fatal(err)
	}
	if got := names(); !slices.Equal(got, []string{"fs"}) {
		t.Fatalf("fs direct: %v", got)
	}
	if err := libraryCmd([]string{"library", "mcp", "router", "fs", "solo"}); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("router fs solo: %v", err)
	}
	if err := libraryCmd([]string{"library", "mcp", "hub", "fs", "shared"}); err != nil {
		t.Fatal(err)
	}
	if got := names(); !slices.Equal(got, []string{"magpie"}) {
		t.Fatalf("fs shared: %v", got)
	}
	if err := libraryCmd([]string{"library", "mcp", "hub", "off"}); err != nil {
		t.Fatal(err)
	}
	if got := names(); !slices.Equal(got, []string{"fs"}) {
		t.Fatalf("hub off: %v", got)
	}
}
