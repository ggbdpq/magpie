package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// A read or backup that fails must stop a new sign-in from replacing the
// original credentials, even when no accounts have been read before.
func TestUnreadLoginsWriteErrorsKeepCredentials(t *testing.T) {
	for _, fault := range []string{"read", "backup"} {
		t.Run(fault, func(t *testing.T) {
			azureHome(t)
			// Freeze the backup's timestamp so an existing directory blocks
			// exactly its filename, without depending on the wall clock.
			synctest.Test(t, func(t *testing.T) {
				p := loginsPath()
				original := `[{"agent":"gemini","user":"old@example.com","auth":{"refresh_token":"old-refresh-token"}}]`
				if fault == "backup" {
					original = strings.TrimSuffix(original, "]") // a file half written
				}
				if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(original), 0o600); err != nil {
					t.Fatal(err)
				}
				lastLoginsMu.Lock()
				lastLogins = nil // a cold start: no cached credentials to recover
				lastLoginsMu.Unlock()
				blocked := p
				if fault == "read" {
					if err := os.Chmod(p, 0); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.Chmod(p, 0o600) })
					if _, err := os.ReadFile(p); err == nil {
						t.Skip("the filesystem does not enforce the unreadable mode")
					} else if !errors.Is(err, os.ErrPermission) {
						t.Fatal(err)
					}
				} else {
					// the folder takes no new file, so the bad copy can't
					// be made
					blocked = p + ".bad-" + time.Now().Format("20060102-150405")
					dir := filepath.Dir(p)
					if err := os.Chmod(dir, 0o500); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
					if f, err := os.Create(filepath.Join(dir, "probe")); err == nil {
						f.Close()
						t.Skip("the filesystem does not enforce the read-only folder")
					}
				}
				err := addGoogleLogin("gemini", "new@example.com", "", googleAuth{RefreshToken: "new-refresh-token", Project: "p"})
				if chmodErr := os.Chmod(p, 0o600); chmodErr != nil {
					t.Fatal(chmodErr)
				}
				b, readErr := os.ReadFile(p)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if string(b) != original {
					t.Fatalf("original credentials overwritten after %s failed: sign-in error = %v", fault, err)
				}
				if err == nil || !strings.Contains(err.Error(), blocked) {
					t.Fatalf("sign-in error = %v, want an error naming %s", err, blocked)
				}
			})
		})
	}
}

// Valid JSON can still be unreadable as saved accounts. On a cold start,
// adding an account must keep those original credentials in a backup.
func TestUnreadLoginsFieldTypesKeptBeforeWrite(t *testing.T) {
	t.Cleanup(AntigravityBuiltinForTest()) // the retired built-in route
	for _, tc := range []struct {
		name string
		text string
	}{
		{"boolean as string", `[{"agent":"antigravity","user":"old@example.com","on":"true","auth":{"refresh_token":"old-refresh-token"}}]`},
		{"invalid time", `[{"agent":"antigravity","user":"old@example.com","seen":"yesterday","auth":{"refresh_token":"old-refresh-token"}}]`},
		{"object instead of list", `{"agent":"antigravity","user":"old@example.com","auth":{"refresh_token":"old-refresh-token"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			googleSandbox(t, &fakeGoogle{})
			if !json.Valid([]byte(tc.text)) {
				t.Fatal("fixture must be syntactically valid JSON")
			}
			p := loginsPath()
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(tc.text), 0o600); err != nil {
				t.Fatal(err)
			}
			lastLoginsMu.Lock()
			lastLogins = nil // magpie just started: no previously read accounts
			lastLoginsMu.Unlock()
			if err := addGoogleLogin("antigravity", "new@example.com", "", googleAuth{RefreshToken: "new-refresh-token", Project: "p"}); err != nil {
				t.Fatal(err)
			}
			kept, err := filepath.Glob(p + ".bad-*")
			if err != nil {
				t.Fatal(err)
			}
			if len(kept) != 1 {
				t.Fatalf("unreadable saved accounts not kept before adding an account: %v", kept)
			}
			b, err := os.ReadFile(kept[0])
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != tc.text {
				t.Fatalf("backup = %q, want original credentials %q", b, tc.text)
			}
			if ls := readLogins(); len(ls) != 1 || ls[0].User != "new@example.com" {
				t.Fatalf("new account not saved: %v", ls)
			}
		})
	}
}

func TestReadableLoginsNotKeptAside(t *testing.T) {
	t.Cleanup(AntigravityBuiltinForTest()) // the retired built-in route
	googleSandbox(t, &fakeGoogle{})
	for _, u := range []string{"a@x.com", "b@x.com"} {
		if err := addGoogleLogin("antigravity", u, "", googleAuth{RefreshToken: "1//" + u, Project: "p"}); err != nil {
			t.Fatal(err)
		}
	}
	kept, err := filepath.Glob(loginsPath() + ".bad-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 0 {
		t.Fatalf("readable saved accounts unnecessarily backed up: %v", kept)
	}
	if ls := readLogins(); len(ls) != 2 {
		t.Fatalf("readable accounts lost when adding an account: %v", ls)
	}
}

// A logins.json that doesn't parse (half written, or written by something
// else) was read as no accounts, and the next account added wrote the
// file anew with that one alone: every saved sign-in gone.
func TestUnreadLoginsNotWrittenOver(t *testing.T) {
	t.Cleanup(AntigravityBuiltinForTest()) // the retired built-in route
	googleSandbox(t, &fakeGoogle{})
	for _, u := range []string{"a@x.com", "b@x.com"} {
		if err := addGoogleLogin("antigravity", u, "", googleAuth{RefreshToken: "1//" + u, Project: "p"}); err != nil {
			t.Fatal(err)
		}
	}
	p := loginsPath()
	good, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	users := func() []string {
		var us []string
		for _, l := range readLogins() {
			us = append(us, l.User)
		}
		slices.Sort(us)
		return us
	}

	// a read that fails: the accounts read before are kept
	os.WriteFile(p, good[:len(good)/2], 0o600)
	if us := users(); strings.Join(us, ",") != "a@x.com,b@x.com" {
		t.Fatalf("a logins.json half there read as %v", us)
	}
	if err := addGoogleLogin("antigravity", "c@x.com", "", googleAuth{RefreshToken: "1//c", Project: "p"}); err != nil {
		t.Fatal(err)
	}
	if us := users(); strings.Join(us, ",") != "a@x.com,b@x.com,c@x.com" {
		t.Fatalf("after an account added: %v", us)
	}

	// nothing read before (magpie just started): the file is kept aside
	// before it is written over. The add above kept its own copy, named by
	// the second it was made, so only the copies made from here count.
	before, _ := filepath.Glob(p + ".bad-*")
	for _, f := range before {
		os.Remove(f)
	}
	half := good[:len(good)/2]
	os.WriteFile(p, half, 0o600)
	lastLoginsMu.Lock()
	lastLogins = nil
	lastLoginsMu.Unlock()
	if err := addGoogleLogin("antigravity", "d@x.com", "", googleAuth{RefreshToken: "1//d", Project: "p"}); err != nil {
		t.Fatal(err)
	}
	kept, _ := filepath.Glob(p + ".bad-*")
	if len(kept) != 1 {
		t.Fatalf("logins.json that didn't parse not kept aside: %v", kept)
	}
	if b, _ := os.ReadFile(kept[0]); string(b) != string(half) {
		t.Fatalf("kept %q", b)
	}
}

// A read of logins.json that keeps failing was logged on every attempt: an
// owner change made each read fail, ~500 identical lines a second filled a
// 2.2 GB log in 8 hours and the disk with it (#7008). The first failure of
// a run is logged, the same error later once per interval with the count
// that was held back, and a read that succeeds again logs a new failure at
// once.
func TestUnreadLoginsLogRateBounded(t *testing.T) {
	azureHome(t)
	synctest.Test(t, func(t *testing.T) {
		p := loginsPath()
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		good := []byte(`[{"agent":"gemini","user":"old@example.com","auth":{"refresh_token":"old-refresh-token"}}]`)
		if err := os.WriteFile(p, good, 0o600); err != nil {
			t.Fatal(err)
		}
		if ls := readLogins(); len(ls) != 1 || ls[0].User != "old@example.com" {
			t.Fatalf("precondition: accounts = %v", ls)
		}
		// another process replaced the file and left it unreadable: the new
		// file time makes every look miss what was parsed, and no read gets
		// in (root's atomic save over a non-root service, #7008)
		if err := os.WriteFile(p, good, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(p, 0o600) })
		if _, err := os.ReadFile(p); err == nil {
			t.Skip("the filesystem does not enforce the unreadable mode")
		}
		var buf bytes.Buffer
		log.SetOutput(&buf)
		log.SetFlags(0)
		t.Cleanup(func() { log.SetOutput(os.Stderr); log.SetFlags(log.LstdFlags) })

		// every read of the file fails: a request, a page poll and a gateway
		// look each came here, and each logged its own line before the fix
		for i := 0; i < 25; i++ {
			readLogins()
		}
		if n := strings.Count(buf.String(), "the accounts read before are kept"); n != 1 {
			t.Fatalf("25 reads of an unreadable logins.json logged %d lines, want 1:\n%s", n, buf.String())
		}

		// the file stays unreadable past the interval: one line saying how
		// many were held back, not one per read
		readLogins()
		time.Sleep(6 * time.Minute)
		readLogins()
		out := buf.String()
		if !strings.Contains(out, "more of the same error while quiet") {
			t.Fatalf("the held-back count is missing after the interval:\n%s", out)
		}
		if n := strings.Count(out, "the accounts read before are kept"); n != 3 {
			t.Fatalf("logged %d lines after the interval, want 3 (first, held-back count, current):\n%s", n, out)
		}

		// the file reads again: a failure after that is logged at once, not
		// suppressed by the run before it
		if err := os.Chmod(p, 0o600); err != nil {
			t.Fatal(err)
		}
		if ls := readLogins(); len(ls) != 1 || ls[0].User != "old@example.com" {
			t.Fatalf("the accounts are gone once the file reads again: %v", ls)
		}
		buf.Reset()
		if err := os.WriteFile(p, good, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0); err != nil {
			t.Fatal(err)
		}
		readLogins()
		if n := strings.Count(buf.String(), "the accounts read before are kept"); n != 1 {
			t.Fatalf("a failure after the file read again logged %d lines, want 1:\n%s", n, buf.String())
		}
	})
}
