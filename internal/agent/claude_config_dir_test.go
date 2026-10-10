package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// With CLAUDE_CONFIG_DIR set, a model picked for Claude Code goes to
// $CLAUDE_CONFIG_DIR/settings.json, the file Claude Code reads (2.1.296
// checked: with the variable set and no ~/.claude, its turn reached the
// base URL that file's env names); nothing is written under ~/.claude, and
// what magpie shows as applied is read back from there.
func TestClaudeConfigDirTakesThePick(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "v", Name: "V", Chat: "https://example.test/v1", Key: "k",
		Models: []string{"claude-opus-5-5"}}); err != nil {
		t.Fatal(err)
	}
	ccdir := filepath.Join(t.TempDir(), "claude-config")
	if err := os.MkdirAll(ccdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ccdir, "settings.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", ccdir)

	a := claude(home)
	if a.Path != filepath.Join(ccdir, "settings.json") || a.Dir != ccdir {
		t.Fatalf("Claude Code's files at %s (%s), want under %s", a.Path, a.Dir, ccdir)
	}
	if err := a.Field("model").Set("v/claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(ccdir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg := string(b); !strings.Contains(cfg, `"model"`) || !strings.Contains(cfg, "ANTHROPIC_BASE_URL") {
		t.Fatalf("$CLAUDE_CONFIG_DIR/settings.json doesn't have the pick:\n%s", cfg)
	}
	if got := claude(home).Field("model").Get(); got != "v/claude-opus-5-5" {
		t.Fatalf("magpie reads the pick back as %q", got)
	}
	if m := StandIn("claude", "v/claude-opus-5-5"); m != "v/claude-opus-5-5" {
		t.Fatalf("the gateway's stand-in for Claude Code is %q, read from somewhere else", m)
	}
	if err := TuneApply(home, "claude", TuneCompact, "claude-opus-5-5", "160000"); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(filepath.Join(ccdir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"autoCompactWindow":160000`) {
		t.Fatalf("tune didn't write $CLAUDE_CONFIG_DIR/settings.json:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		var names []string
		filepath.WalkDir(filepath.Join(home, ".claude"), func(p string, _ os.DirEntry, _ error) error {
			names = append(names, p)
			return nil
		})
		t.Fatalf("written under ~/.claude with CLAUDE_CONFIG_DIR set: %v", names)
	}
}
