package provider

import (
	"os"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// A non-own plugin account whose vendor-side display name changes keeps its
// per-account proxy (and other per-account settings) and stable ID instead of
// silently falling back to the provider's proxy (#5729). When providers.json
// cannot be read, the rename waits rather than stranding the proxy under the
// old name.
func TestPluginRenameKeepsProxyAndSettings(t *testing.T) {
	signIn(t)
	renameFailedAt = time.Time{}

	const (
		provProxy = "http://127.0.0.1:9001"
		acctProxy = "socks5://127.0.0.1:9002"
	)
	if err := store(file{Providers: []Provider{{
		ID:             "fakeco",
		Proxy:          provProxy,
		AccountProxies: map[string]string{"alice": acctProxy},
		AccountCaps:    map[string]int{"alice": 80},
	}}}); err != nil {
		t.Fatal(err)
	}
	saveLogins(t, savedLogin{Agent: "plugin:fakeco", User: "alice", Home: "fakeco", On: true})

	ppBefore := plugin.Provider{
		ID:       "fakeco",
		Name:     "FakeCo",
		SignedIn: true,
		Accounts: []plugin.Account{{Key: "fakeco", Type: "oauth", AccountID: "alice"}},
	}
	before := pluginLogins(ppBefore)
	if len(before) != 1 || before[0].User != "alice" {
		t.Fatalf("before = %+v", before)
	}
	stableID := before[0].saved.ID
	if got := ProxyOfLogin("fakeco", "alice"); got != acctProxy {
		t.Fatalf("before rename proxy = %q, want %q", got, acctProxy)
	}

	// Vendor renames the account from "alice" to "alice-new".
	ppAfter := plugin.Provider{
		ID:       "fakeco",
		Name:     "FakeCo",
		SignedIn: true,
		Accounts: []plugin.Account{{Key: "fakeco", Type: "oauth", AccountID: "alice-new"}},
	}
	after := pluginLogins(ppAfter)
	if len(after) != 1 || after[0].User != "alice-new" {
		t.Fatalf("after = %+v", after)
	}
	if after[0].saved.ID != stableID {
		t.Errorf("stable ID changed on rename: %q -> %q", stableID, after[0].saved.ID)
	}
	if got := ProxyOfLogin("fakeco", "alice-new"); got != acctProxy {
		t.Errorf("ProxyOfLogin after rename = %q, want %q (fell back to provider proxy)", got, acctProxy)
	}
	if got := pluginProvider(ppAfter, after[0]).ProxyChoice(); got != acctProxy {
		t.Errorf("Provider.ProxyChoice after rename = %q, want %q", got, acctProxy)
	}
	if got := AccountCapOf("fakeco", "alice-new"); got != 80 {
		t.Errorf("AccountCapOf after rename = %d, want 80", got)
	}

	// Fail-closed: if providers.json is corrupt (with no good backup) when
	// another rename arrives, logins.json keeps the old name until
	// providers.json can be read again.
	_ = os.Remove(Path() + ".bak")
	if err := os.WriteFile(Path(), []byte(`{"providers": [`), 0o600); err != nil {
		t.Fatal(err)
	}
	ppCorrupt := plugin.Provider{
		ID:       "fakeco",
		Name:     "FakeCo",
		SignedIn: true,
		Accounts: []plugin.Account{{Key: "fakeco", Type: "oauth", AccountID: "alice-third"}},
	}
	pluginLogins(ppCorrupt)
	if ls := readLogins(); len(ls) != 1 || ls[0].User != "alice-new" {
		t.Fatalf("logins.json renamed while providers.json was unreadable: %+v", ls)
	}
	if raw, _ := os.ReadFile(Path()); string(raw) != `{"providers": [` {
		t.Fatalf("corrupt providers.json was overwritten: %s", raw)
	}

	// Once providers.json is readable again and the retry window opens, the
	// rename completes and moves the proxy with it.
	if err := store(file{Providers: []Provider{{
		ID:             "fakeco",
		Proxy:          provProxy,
		AccountProxies: map[string]string{"alice-new": acctProxy},
	}}}); err != nil {
		t.Fatal(err)
	}
	renameFailedAt = time.Time{}
	recovered := pluginLogins(ppCorrupt)
	if len(recovered) != 1 || recovered[0].User != "alice-third" {
		t.Fatalf("recovered = %+v", recovered)
	}
	if got := ProxyOfLogin("fakeco", "alice-third"); got != acctProxy {
		t.Fatalf("ProxyOfLogin after recovery = %q, want %q", got, acctProxy)
	}
}
