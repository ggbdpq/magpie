package cli

// wssz527 (#4030): `magpie provider pool <id> --keep on` says a transient
// rest — a rate limit, a failure — is no reason to move the pool's
// accounts, and --keep off says it is again. Anything else is refused.
import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/provider"
)

func TestPoolKeepSaysItsPoolKeepsThroughARest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("COPILOT_HOME", filepath.Join(home, ".copilot"))
	logins := filepath.Join(filepath.Dir(provider.Path()), "logins.json")
	if err := os.MkdirAll(filepath.Dir(logins), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logins, []byte(`[
  {"agent": "copilot", "user": "mona", "seen": "2026-10-05T10:00:00Z", "on": true, "auth": {"oauth_token": "gho_m"}},
  {"agent": "copilot", "user": "hubot", "seen": "2026-10-06T10:00:00Z", "on": true, "auth": {"oauth_token": "gho_h"}}
]`), 0o600); err != nil {
		t.Fatal(err)
	}
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)
	t.Cleanup(agent.SyncCatalogSettled) // the pool's save syncs the agents' files behind it

	if _, err := said(t, func() error { return poolCmd([]string{"copilot", "--keep", "on"}) }); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("copilot")
	if err != nil {
		t.Fatal(err)
	}
	if !p.KeepThroughRest {
		t.Fatal("--keep on was not kept")
	}
	if _, err := said(t, func() error { return poolCmd([]string{"copilot", "--keep", "off"}) }); err != nil {
		t.Fatal(err)
	}
	if p, err = provider.Find("copilot"); err != nil {
		t.Fatal(err)
	}
	if p.KeepThroughRest {
		t.Fatal("--keep off was not kept")
	}
	if _, err := said(t, func() error { return poolCmd([]string{"copilot", "--keep", "maybe"}) }); err == nil {
		t.Fatal("maybe was taken for on or off")
	}
}
