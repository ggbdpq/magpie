package provider

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Codex's only account is signed out when it is removed, as codex logout
// does (mamba on Discord); one with another saved signs Codex in to that
// one first (vincentzhang1_55530 on Discord: a Team account removed beside
// a Plus one left Codex at its sign-in screen); and ForgetAccount, which
// takes the one in use last, signs Codex out too.
func TestCodexSignOutLastAccount(t *testing.T) {
	home := signIn(t)
	auth := filepath.Join(home, ".codex", "auth.json")
	rememberLogins(true)
	var signedIn *SignedInError

	// another saved: the one signed in to goes, and Codex is signed in to
	// the other, its own sign-in back in auth.json
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)
	if err := ForgetLogin("codex", "work@example.com"); err != nil {
		t.Fatalf("the one in use with another saved: %v", err)
	}
	live, ok := liveLogin("codex")
	if !ok || live.User != "me@example.com" {
		t.Fatalf("Codex signed in to %q (%v), want me@example.com", live.User, ok)
	}
	if b, _ := os.ReadFile(auth); strings.Contains(string(b), "r-work") {
		t.Fatalf("auth.json still the removed one's: %s", b)
	}
	rememberLogins(true)
	if ls := Logins("codex"); len(ls) != 1 || ls[0].User != "me@example.com" || !ls[0].Active {
		t.Fatalf("listed: %+v", ls)
	}

	// the one left is signed out
	if err := ForgetLogin("codex", "me@example.com"); err != nil {
		t.Fatalf("the only account: %v", err)
	}
	if _, err := os.Stat(auth); !os.IsNotExist(err) {
		t.Fatalf("auth.json still there: %v", err)
	}
	if ls := Logins("codex"); len(ls) != 0 {
		t.Fatalf("still listed: %+v", ls)
	}
	rememberLogins(true)
	if ls := Logins("codex"); len(ls) != 0 {
		t.Fatalf("remembered again: %+v", ls)
	}

	// signed in but not saved yet: signed out all the same
	codexSignIn(t, home, "new@example.com", "r-new")
	if err := ForgetLogin("codex", "new@example.com"); err != nil {
		t.Fatalf("unsaved: %v", err)
	}
	if _, err := os.Stat(auth); !os.IsNotExist(err) {
		t.Fatalf("auth.json still there: %v", err)
	}

	// an account not signed in to and not saved is still unknown
	if err := ForgetLogin("codex", "nobody@example.com"); err == nil || errors.As(err, &signedIn) {
		t.Fatalf("unknown: %v", err)
	}
}

func TestForgetAccountSignsCodexOut(t *testing.T) {
	home := signIn(t)
	rememberLogins(true)
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)
	if err := ForgetAccount("codex"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("auth.json still there: %v", err)
	}
	if ls := Logins("codex"); len(ls) != 0 {
		t.Fatalf("saved still: %+v", ls)
	}
}

// #5916: the Add sheet's "Removed from magpie" row of Codex, its Sign out…
// (ForgetAccount) — the ask says Codex itself is signed out too, as codex
// logout does. A sign-in none of magpie's saved copies named — removed from
// magpie before any look saved it, or Codex signed in to another account
// itself after, which magpie then saves nothing of — used to be left where
// it was: ForgetAccount said done, and the row stood there "still signed
// in", with a Sign out… that would say done and change nothing, every time.
func TestForgetAccountSignsOutASignInNoSavedCopyNamed(t *testing.T) {
	home := signIn(t) // no rememberLogins: logins.json holds no codex account
	auth := filepath.Join(home, ".codex", "auth.json")
	// a look just happened (the page reads every few seconds): the sign-in
	// isn't due a saving, and once removed magpie saves nothing of Codex
	loginsSeenAt.Store(time.Now().UnixNano())
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	if err := QuietAccount("codex"); err != nil {
		t.Fatal(err)
	}
	if x := Excluded(); len(x) != 1 || !x[0].Quiet || x[0].SignedOut {
		t.Fatalf("removed: %+v", x)
	}
	if err := ForgetAccount("codex"); err != nil {
		t.Fatal(err)
	}
	// the row's reading: gone, not still "still signed in"
	if x := Excluded(); len(x) != 0 {
		t.Fatalf("still listed: %+v", x)
	}
	if _, err := os.Stat(auth); !os.IsNotExist(err) {
		t.Fatalf("auth.json still there: %v", err)
	}
	if ls := Logins("codex"); len(ls) != 0 {
		t.Fatalf("saved still: %+v", ls)
	}
}

// removed from magpie, the user signed Codex in to another account
// themselves; magpie saves nothing of it (accountRemoved), so no saved copy
// names the sign-in Codex holds now — the Sign out… on the removed row
// signs that one out too, not only the copies it kept.
func TestForgetAccountSignsOutTheSignInItMovedOnTo(t *testing.T) {
	home := signIn(t) // me@example.com, saved
	auth := filepath.Join(home, ".codex", "auth.json")
	rememberLogins(true)
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	if err := QuietAccount("codex"); err != nil {
		t.Fatal(err)
	}
	codexSignIn(t, home, "work@example.com", "r-work") // never saved: removed
	if x := Excluded(); len(x) != 1 || !x[0].Quiet {
		t.Fatalf("removed: %+v", x)
	}
	if err := ForgetAccount("codex"); err != nil {
		t.Fatal(err)
	}
	if x := Excluded(); len(x) != 0 {
		t.Fatalf("still listed: %+v", x)
	}
	if _, err := os.Stat(auth); !os.IsNotExist(err) {
		t.Fatalf("auth.json still there: %v", err)
	}
	if ls := Logins("codex"); len(ls) != 0 {
		t.Fatalf("saved still: %+v", ls)
	}
}
