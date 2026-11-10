package agent

import (
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// pctSandbox is a HOME with provider v's glm at a 1Mi window (the kind of
// model the user declares a big window for) and gpt at 272K.
func pctSandbox(t *testing.T) (home, path string) {
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "v", Name: "V", Chat: "http://127.0.0.1:1/v1", Key: "k",
		Models: []string{"glm", "gpt"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("v", "http://127.0.0.1:1/v1", []catalog.Model{
		{ID: "glm", Context: 1048576}, {ID: "gpt", Context: 272000},
	}); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{"theme":"dark"}`)
	return home, path
}

// TestClaudeDesktopCompactPctDeclaration (#5969): while Claude Desktop is
// on magpie, a user who declares where compaction goes in Claude Code's
// settings (CLAUDE_AUTOCOMPACT_PCT_OVERRIDE, a share of the window, with
// CLAUDE_CODE_MAX_CONTEXT_TOKENS the window itself) keeps that policy:
// magpie's default working window is not written over the top-level
// autoCompactWindow again, on a restart or a re-apply, so Desktop runs the
// declared window instead of showing 272K the user never chose. magpie
// never writes the pct variable, so one present is the user's own.
func TestClaudeDesktopCompactPctDeclaration(t *testing.T) {
	home, path := pctSandbox(t)
	a := claude(home)
	window := func() string { v, _ := edit.GetJSON(path, "env."+claudeContextEnv); return v }
	pct := func() string { v, _ := edit.GetJSON(path, "env."+claudeCompactPctEnv); return v }
	top := func() string { v, _ := edit.GetJSON(path, "autoCompactWindow"); return v }
	def := func() string { return stashShared()["claude.compact_default"] }

	// magpie's desktop plan first: the largest window is told, the
	// working window goes in the top-level autoCompactWindow
	if err := claudeDesktop(home).Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("v/glm"); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if window() != "1048576" || top() != "272000" {
		t.Fatalf("wired: window %q top %q", window(), top())
	}
	if def() != "272000" {
		t.Fatalf("wired: stash compact_default %q", def())
	}

	// the user declares the policy in Claude Code's settings: the window
	// they run (the one magpie wrote is the same 1Mi they declare) and
	// compaction at a share of it; the top-level default they delete
	if err := edit.SetJSON(path,
		edit.KV{Path: "env." + claudeCompactPctEnv, Value: "80"},
	); err != nil {
		t.Fatal(err)
	}
	if err := edit.DelJSON(path, "autoCompactWindow"); err != nil {
		t.Fatal(err)
	}

	// Desktop restarts, magpie applies again: the declaration stands, the
	// default does not come back
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if v := top(); v != "" {
		t.Fatalf("the deleted top-level autoCompactWindow was written again: %q", v)
	}
	if def() != "" {
		t.Fatalf("stash compact_default %q came back", def())
	}
	if window() != "1048576" || pct() != "80" {
		t.Fatalf("the user's declaration was touched: window %q pct %q", window(), pct())
	}
	if v, _ := edit.GetJSON(path, "env."+claudeCompactEnv); v != "" {
		t.Fatalf("%s %q would overrule the declared share", claudeCompactEnv, v)
	}

	// without the declaration, the window value in the env is the user's
	// own now (magpie's mark went with the dropped plan), and stands: the
	// default management stays out while it does, and comes back once the
	// user clears the env too, as with any value of theirs
	if err := edit.DelJSON(path, "env."+claudeCompactPctEnv); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if top() != "" || window() != "1048576" {
		t.Fatalf("the user's window value stands: top %q window %q", top(), window())
	}
	if err := edit.DelJSON(path, "env."+claudeContextEnv); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if top() != "272000" || window() != "1048576" {
		t.Fatalf("default management back: top %q window %q", top(), window())
	}
}

// TestClaudeCompactPctGivesWay (#5969): Claude Code compacts at the share
// of the window the user declared (CLAUDE_AUTOCOMPACT_PCT_OVERRIDE), so
// magpie's default working window gives way to it (#876), as to a window
// the user typed: no CLAUDE_CODE_AUTO_COMPACT_WINDOW is written while the
// declaration is there, and the user's variable is never touched.
func TestClaudeCompactPctGivesWay(t *testing.T) {
	home, path := pctSandbox(t)
	a := claude(home)
	compact := func() string { v, _ := edit.GetJSON(path, "env."+claudeCompactEnv); return v }
	pct := func() string { v, _ := edit.GetJSON(path, "env."+claudeCompactPctEnv); return v }
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	// the default first: the working window
	must(a.Field("model").Set("v/glm"))
	if compact() != "272000" {
		t.Fatalf("on magpie: %q", compact())
	}

	// the user declares a share of the window: magpie's own default goes,
	// the declaration stands
	must(edit.SetJSON(path, edit.KV{Path: "env." + claudeCompactPctEnv, Value: "80"}))
	must(a.Sync())
	if compact() != "" {
		t.Fatalf("declared share held to the working window: %q", compact())
	}
	if pct() != "80" {
		t.Fatalf("the user's variable was touched: %q", pct())
	}

	// the declaration gone, the default is written again
	must(edit.DelJSON(path, "env."+claudeCompactPctEnv))
	must(a.Sync())
	if compact() != "272000" {
		t.Fatalf("declaration gone: %q", compact())
	}
}
