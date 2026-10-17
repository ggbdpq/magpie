package provider

// A read of the keychain item Claude Code keeps in a saved account's
// config directory (claudeDirService) refused in a way that may have
// shown the user a dialog isn't asked again inside keychainDeniedTTL,
// as the home item's read isn't (#2408). A saved account's sign-in is
// read back on every request made for it, so without the pause an
// unanswered denial would prompt on each one. A fake `security` answers
// the way a refused read does (51, not 44, the item not being there),
// and logs each ask.

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// fakeSecurityDeniedFor differs from fakeSecurityDenied in standing in on
// Windows too: LookPath there only takes a program with an extension it
// knows, so the ask is logged from a .bat.
func fakeSecurityDeniedFor(t *testing.T) func() int {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "asked.log")
	name, script := "security", "#!/bin/sh\necho \"$*\" >> "+log+"\nexit 51\n"
	if runtime.GOOS == "windows" {
		name = "security.bat"
		script = "@echo %* >> \"" + log + "\"\r\nexit /b 51\r\n"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() int {
		b, _ := os.ReadFile(log)
		return bytes.Count(b, []byte("\n"))
	}
}

func TestDeniedClaudeDirReadIsNotAskedAgain(t *testing.T) {
	oldKeychain := claudeKeychain
	claudeKeychain = true
	defer func() { claudeKeychain = oldKeychain }()
	asks := fakeSecurityDeniedFor(t)
	dir := t.TempDir()
	claudeDirAsk.Lock()
	claudeDirAsk.m = map[string]time.Time{}
	claudeDirAsk.Unlock()
	defer func() {
		claudeDirAsk.Lock()
		claudeDirAsk.m = map[string]time.Time{}
		claudeDirAsk.Unlock()
	}()

	if _, ok := readClaudeDir(dir); ok {
		t.Fatal("a refused read is no sign-in")
	}
	if n := asks(); n != 1 {
		t.Fatalf("the first read asks the keychain once, asked %d times", n)
	}

	// still inside the denial's pause: not asked again; what magpie wrote
	// stands in the file beside the item, which here holds none
	if _, ok := readClaudeDir(dir); ok {
		t.Fatal("a refused read is no sign-in")
	}
	if n := asks(); n != 1 {
		t.Fatalf("a refused read was asked again inside the pause: %d asks", n)
	}

	// past the pause, the item is asked about again
	claudeDirAsk.Lock()
	claudeDirAsk.m[dir] = time.Now().Add(-keychainDeniedTTL - time.Second)
	claudeDirAsk.Unlock()
	if _, ok := readClaudeDir(dir); ok {
		t.Fatal("a refused read is no sign-in")
	}
	if n := asks(); n != 2 {
		t.Fatalf("a read past the pause asks the keychain again, asked %d times", n)
	}
}

// The item not being there (44) asks nobody, so it is never a pause: a
// read of a directory whose item is absent is free to ask again, and a
// pause on one directory doesn't hold another's reads back.
func TestAbsentClaudeDirItemKeepsAsking(t *testing.T) {
	oldKeychain := claudeKeychain
	claudeKeychain = true
	defer func() { claudeKeychain = oldKeychain }()
	asks := fakeSecurityAbsentFor(t)
	dir := t.TempDir()
	claudeDirAsk.Lock()
	claudeDirAsk.m = map[string]time.Time{}
	claudeDirAsk.Unlock()
	defer func() {
		claudeDirAsk.Lock()
		claudeDirAsk.m = map[string]time.Time{}
		claudeDirAsk.Unlock()
	}()

	for i := 0; i < 2; i++ {
		if _, ok := readClaudeDir(dir); ok {
			t.Fatal("an absent item is no sign-in")
		}
	}
	if n := asks(); n != 2 {
		t.Fatalf("an absent item's reads are not paused: asked %d times over two reads", n)
	}
}

// fakeSecurityAbsentFor answers 44, the item not being there, which asks
// nobody.
func fakeSecurityAbsentFor(t *testing.T) func() int {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "asked.log")
	name, script := "security", "#!/bin/sh\necho \"$*\" >> "+log+"\nexit 44\n"
	if runtime.GOOS == "windows" {
		name = "security.bat"
		script = "@echo %* >> \"" + log + "\"\r\nexit /b 44\r\n"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() int {
		b, _ := os.ReadFile(log)
		return bytes.Count(b, []byte("\n"))
	}
}
