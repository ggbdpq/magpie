package library

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A dsh profile created after magpie already synced web/desktop/headless
// (such as Cindy's acp profile, #7839) must receive magpie's MCP entries
// on the next sync or routed-server save, even when web's entry is unchanged.
func TestDshNewProfileGetsMCP(t *testing.T) {
	h := sandbox(t)
	d := filepath.Join(h, "dsh-home")
	t.Setenv("DSH_HOME", d)
	head := "# Your patch layer for this dsh profile\n"
	web := filepath.Join(d, "profiles/web/cordis.patch.yml")
	desk := filepath.Join(d, "profiles/desktop/cordis.patch.yml")
	headless := filepath.Join(d, "profiles/headless/cordis.patch.yml")
	for _, p := range []string{web, desk, headless} {
		write(t, p, head+"[]\n")
	}

	ok(t)(SetMCPHub(true))
	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@mcp/fs"}, Agents: []string{"dsh"}}))
	for _, p := range []string{web, desk, headless} {
		if !strings.Contains(read(t, p), dshMCPPlugin) {
			t.Fatalf("%s missing %s after initial save:\n%s", p, dshMCPPlugin, read(t, p))
		}
	}

	// Cindy creates ~/.dsh/profiles/acp/cordis.patch.yml after initial sync.
	acp := filepath.Join(d, "profiles/acp/cordis.patch.yml")
	write(t, acp, head+"[]\n")

	// Adding another routed server keeps web's hub entry unchanged, yet acp
	// must still receive the hub entry.
	ok(t)(SaveServer("", Server{Name: "exa", Transport: "stdio", Command: "npx", Args: []string{"-y", "@mcp/exa"}, Agents: []string{"dsh"}}))
	got, err := (&mcpFile{Path: acp, Format: fmtDsh}).read()
	if err != nil {
		t.Fatalf("read acp: %v", err)
	}
	if got[HubEntry] == nil {
		t.Fatalf("acp profile missing hub entry %q after routed save:\n%s", HubEntry, read(t, acp))
	}

	// A second sync when every profile matches must be a no-op.
	res, err := Sync()
	if err != nil || len(res.Problems) != 0 {
		t.Fatalf("second sync: %+v %v", res, err)
	}
	if slices.Contains(res.Changed, "dsh") {
		t.Fatalf("second sync unexpectedly rewrote dsh: %+v", res.Changed)
	}

	// Direct (non-hub) mode: a profile created later (tui) also gets synced.
	ok(t)(SetMCPHub(false))
	tui := filepath.Join(d, "profiles/tui/cordis.patch.yml")
	write(t, tui, head+"[]\n")
	ok(t)(Sync())
	got, err = (&mcpFile{Path: tui, Format: fmtDsh}).read()
	if err != nil {
		t.Fatalf("read tui: %v", err)
	}
	if got["fs"] == nil || got["exa"] == nil {
		t.Fatalf("tui profile missing direct servers after Sync:\n%s", read(t, tui))
	}
}

// Secondary dsh profiles participate in cleanup (holds), discovery (read),
// and keep unreadable/corrupt patch files untouched (failure-read semantics).
func TestDshSecondaryProfileHoldsReadAndCorrupt(t *testing.T) {
	h := sandbox(t)
	d := filepath.Join(h, "dsh-home")
	t.Setenv("DSH_HOME", d)
	head := "# Your patch layer for this dsh profile\n"
	web := filepath.Join(d, "profiles/web/cordis.patch.yml")
	acp := filepath.Join(d, "profiles/acp/cordis.patch.yml")
	write(t, web, head+"[]\n")
	write(t, acp, head+"[]\n")

	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@mcp/fs"}, Agents: []string{"dsh"}}))

	// Simulate manual removal from web while acp still holds fs; unticking
	// must still remove fs from acp via holds().
	write(t, web, head+"[]\n")
	ok(t)(RemoveServer("fs"))
	if strings.Contains(read(t, acp), "serverName: \"fs\"") || strings.Contains(read(t, acp), "serverName: fs") {
		t.Fatalf("acp still holds fs after RemoveServer:\n%s", read(t, acp))
	}

	// A user-added server only in acp is discovered by read() and removed by
	// RemoveFoundServer().
	write(t, acp, head+"- insert:\n    - id: mcp-solo\n      name: '@deepseek-ai/dsh-mcp-client'\n      config:\n        serverName: solo\n        transport: stdio\n        command: solo-mcp\n")
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(v.FoundServers, func(f Found) bool { return f.Server.Name == "solo" && slices.Contains(f.Server.Agents, "dsh") }) {
		t.Fatalf("FoundServers missing solo from acp: %+v", v.FoundServers)
	}
	ok(t)(RemoveFoundServer("solo"))
	if strings.Contains(read(t, acp), "solo") {
		t.Fatalf("acp still has solo after RemoveFoundServer:\n%s", read(t, acp))
	}

	// Failure-read semantics: a corrupt secondary profile is treated as unknown
	// and never overwritten when healthy profiles are already in sync.
	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@mcp/fs"}, Agents: []string{"dsh"}}))
	broken := filepath.Join(d, "profiles/broken/cordis.patch.yml")
	corrupt := "not-a-list: true\n"
	write(t, broken, corrupt)
	res, err := Sync()
	if err != nil || len(res.Problems) != 0 {
		t.Fatalf("sync with corrupt secondary profile: %+v %v", res, err)
	}
	if got := read(t, broken); got != corrupt {
		t.Fatalf("corrupt profile was modified:\ngot:  %q\nwant: %q", got, corrupt)
	}
}
