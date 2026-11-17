package library

import (
	"path/filepath"
	"strings"
	"testing"
)

// A profile whose patch list lost magpie's insert (one rewritten by its
// own, or created after the last write) is never backfilled: the checkers
// look at the files of Also only for the formats they name, and dsh (and
// ds-bot, the same shape) was not one of them — has() was vacuously true,
// so sync skipped the write while the library still said the server was
// applied (#6858). putEntry and del already write every profile's file;
// the front file matching is what kept the others out.
func TestDshBackfillsAProfileMissingTheInsert(t *testing.T) {
	h := sandbox(t)
	d := filepath.Join(h, "dsh-home")
	t.Setenv("DSH_HOME", d)
	web, desk := filepath.Join(d, "profiles/web/cordis.patch.yml"), filepath.Join(d, "profiles/desktop/cordis.patch.yml")
	head := "# Your patch layer for this dsh profile\n"
	write(t, web, head+"[]\n")
	write(t, desk, head+"[]\n")
	write(t, filepath.Join(d, "AGENTS.md"), "# Mine\n")
	fs := Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@mcp/fs"}, Agents: []string{"dsh"}}
	ok(t)(SaveServer("", fs))
	for _, p := range []string{web, desk} {
		if s := read(t, p); !strings.Contains(s, "- insert: # magpie") {
			t.Fatalf("%s has no insert after the first write:\n%s", p, s)
		}
	}
	// the desktop profile's own rewrite takes magpie's insert with it; a
	// profile created after the last write looks the same
	write(t, desk, head+"[]\n")
	ok(t)(SaveServer("fs", fs))
	if s := read(t, desk); !strings.Contains(s, "- insert: # magpie") {
		t.Fatalf("the desktop profile's insert was not written back:\n%s", s)
	}
	if s := read(t, web); strings.Count(s, "- id: magpie-mcp-fs") != 1 {
		t.Fatalf("the front file's insert is not what it was:\n%s", s)
	}
}

// A server of the user's that only a profile in Also holds is discovered
// as dsh's own: read() scanned Also for the formats it names, and dsh was
// not one of them, so such a server was invisible (#6858).
func TestDshReadsAUsersServerInAnotherProfile(t *testing.T) {
	h := sandbox(t)
	d := filepath.Join(h, "dsh-home")
	t.Setenv("DSH_HOME", d)
	web, desk := filepath.Join(d, "profiles/web/cordis.patch.yml"), filepath.Join(d, "profiles/desktop/cordis.patch.yml")
	head := "# Your patch layer for this dsh profile\n"
	write(t, web, head+"[]\n")
	write(t, desk, head+"- insert:\n"+
		"    - id: mcp-engram\n      name: '@deepseek-ai/dsh-mcp-client'\n      config:\n        serverName: engram\n        transport: stdio\n        command: engram\n")
	write(t, filepath.Join(d, "AGENTS.md"), "# Mine\n")
	tg := targetByID("dsh")
	if tg == nil || tg.MCP == nil {
		t.Fatal("no dsh target")
	}
	got, err := tg.MCP.read()
	if err != nil {
		t.Fatal(err)
	}
	if s := got["engram"]; s == nil || s.Command != "engram" {
		t.Fatalf("engram, held by the desktop profile only, read as %+v", s)
	}
}
