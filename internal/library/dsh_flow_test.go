package library

import (
	"path/filepath"
	"strings"
	"testing"
)

// A patch list in flow style (#445) is read and written like a block one:
// the user's rows stay, magpie's server goes in as an insert of its own.
func TestDshFlowPatchList(t *testing.T) {
	h := sandbox(t)
	p := filepath.Join(h, ".dsh/profiles/web/cordis.patch.yml")
	write(t, p, "# Your patch layer for this dsh profile\n[ { id: some-plugin, disabled: false } ]\n")
	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@mcp/fs"}, Agents: []string{"dsh"}}))
	s := read(t, p)
	for _, want := range []string{"# Your patch layer for this dsh profile\n- id: some-plugin\n  disabled: false\n", "- insert: # magpie\n    - id: magpie-mcp-fs\n", `serverName: "fs"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in\n%s", want, s)
		}
	}
}

// A patch list that starts with a YAML document start marker (---) is read
// and written like one without it, the marker kept at the head (#2667).
func TestDshDocStartPatchList(t *testing.T) {
	h := sandbox(t)
	p := filepath.Join(h, ".dsh/profiles/web/cordis.patch.yml")
	// the reporter's file (#2667), down to its quoting and trailing spaces
	write(t, p, "---\n- \n  id: \"ui-settings-general\"\n  name: \"@deepseek-ai/dsh-client-ui-settings-general\"\n  config: \n    welcomeNoticeVersion: \"2026-09-28.1\"\n")
	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@mcp/fs"}, Agents: []string{"dsh"}}))
	s := read(t, p)
	if !strings.HasPrefix(s, "---\n") {
		t.Fatalf("the document start marker lost:\n%s", s)
	}
	for _, want := range []string{"id: \"ui-settings-general\"", "- insert: # magpie\n    - id: magpie-mcp-fs\n"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in\n%s", want, s)
		}
	}
}
