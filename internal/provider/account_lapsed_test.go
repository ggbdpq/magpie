package provider

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/testenv"
)

// A plugin account whose sign-in the vendor refused (Lapsed) is asked
// after the healthy ones, not in its rank: tried in its place, its 401
// came back as the conversation's own while the providers the agent
// asked were never tried — three Codex conversations, 105 requests, all
// on one dead WorkBuddy account in seven minutes (#7186). With no
// healthy account left they are asked as before, in their places: the
// refusal they answer is all there is to say.
func TestAccountsForSendsLapsedLast(t *testing.T) {
	lapsedPluginAccounts(t, map[string]string{"a@fake": "refused", "b@fake": "", "c@fake": ""})
	id := PluginID("fakeco")
	p, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	out, left, barred := p.AccountsFor("fake-claude", nil)
	if barred != nil {
		t.Fatalf("barred %v", loginUsersOf(barred))
	}
	if got := loginUsersOf(out); !reflect.DeepEqual(got, []string{"b@fake", "c@fake"}) {
		t.Fatalf("out = %v, want the healthy ones only", got)
	}
	if got := loginUsersOf(left); !reflect.DeepEqual(got, []string{"a@fake"}) {
		t.Fatalf("left = %v, want the lapsed one alone", got)
	}
	// and with none healthy, they are asked as before, in their places
	lapsedPluginAccounts(t, map[string]string{"a@fake": "refused", "b@fake": "refused", "c@fake": "refused"})
	p, _ = Find(id)
	out, left, barred = p.AccountsFor("fake-claude", nil)
	if got := loginUsersOf(out); !slices.Equal(got, []string{"a@fake", "b@fake", "c@fake"}) {
		t.Fatalf("with none healthy, out = %v, want them all", got)
	}
	if len(left)+len(barred) != 0 {
		t.Fatalf("left %v barred %v", loginUsersOf(left), loginUsersOf(barred))
	}
}

func lapsedPluginAccounts(t *testing.T, lapsed map[string]string) {
	t.Helper()
	bun := testenv.Bun(t)
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	var rows []savedLogin
	for user, why := range lapsed {
		k, err := plugin.Import(ctx, "fakeco", map[string]any{"type": "oauth", "refresh": "r-" + user, "access": "a", "expires": 9e15, "accountId": user})
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, savedLogin{Agent: "plugin:fakeco", User: user, Home: k, On: true, Lapsed: why})
	}
	saveLogins(t, rows...)
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
}

func loginUsersOf(as []AccountOn) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.Account.User)
	}
	return out
}
