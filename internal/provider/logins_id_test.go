package provider

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

// An account's stable id (#905) is set once its login is written and kept
// through a rename: a gateway key held to accounts names it by that, not
// by a name that moves. Until the logins are written, one made of the
// name stands in.
func TestLoginIDStable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := writeLogins([]savedLogin{{Agent: "codex", User: "me@example.com"}, {Agent: "codex", User: "spare@example.com", On: true}}); err != nil {
		t.Fatal(err)
	}
	ls := readLogins()
	if ls[0].ID == "" || ls[1].ID == "" || ls[0].ID == ls[1].ID {
		t.Fatalf("written without ids: %+v", ls)
	}
	id := ls[0].ID
	ls[0].User = "renamed@example.com"
	if err := writeLogins(ls); err != nil {
		t.Fatal(err)
	}
	if got := LoginID("codex", "renamed@example.com"); got != id {
		t.Fatalf("a rename moved the id: %s, was %s", got, id)
	}
	// a name no account has is one of no account's: another id, matching
	// nothing a candidate carries
	if got := LoginID("codex", "other@example.com"); got == id {
		t.Fatal("another account had the same id")
	}
}

// The id also stays the account's when the agent's own sign-in is
// remembered over its record (rememberLogins, upsertLogin) and when two
// records of one account are folded (dedupeLogins): the sign-in read from
// the agent carries no id, and the record taking its place had the id
// stamped anew from the name — which a rename moves (#2642: a Codex seat
// renamed, " · Team" or its workspace after it, was no longer the account
// a conversation's native compaction was sealed by, nor the one a gateway
// key is held to).
func TestLoginIDKeptThroughALiveSignIn(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"me@example.com","https://api.openai.com/auth":{"chatgpt_account_id":"acct-1"}}`))
	auth := json.RawMessage(`{"tokens":{"id_token":"h.` + claims + `.s","account_id":"acct-1"}}`)
	if err := writeLogins([]savedLogin{{Agent: "codex", User: "me@example.com", ID: "kept-id", On: true, Auth: auth}}); err != nil {
		t.Fatal(err)
	}
	// the seat Codex is on, renamed: the same email in the same workspace
	ls := upsertLogin(readLogins(), savedLogin{Agent: "codex", User: "me@example.com · Team", Auth: auth, Seen: time.Now()})
	if len(ls) != 1 || ls[0].User != "me@example.com · Team" {
		t.Fatalf("the sign-in didn't take the record's place: %+v", ls)
	}
	if ls[0].ID != "kept-id" {
		t.Fatalf("the sign-in moved the id: %q", ls[0].ID)
	}
	ls = dedupeLogins([]savedLogin{
		{Agent: "codex", User: "me@example.com", ID: "kept-id", Auth: auth, Seen: time.Now().Add(-time.Minute)},
		{Agent: "codex", User: "me@example.com · Team", Auth: auth, Seen: time.Now()},
	})
	if len(ls) != 1 || ls[0].ID != "kept-id" {
		t.Fatalf("the fold moved the id: %+v", ls)
	}
}
