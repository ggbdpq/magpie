package access

import (
	"bytes"
	"os"
	"testing"
)

// caller-keys.json zeroed by a crash mid-write (#1505 — the same Windows
// power loss that left logins.json, plugin-auth.json and mcp-signins.json
// all zero) is its last good generation, so the gateway's keys keep
// answering; with none, a change is refused and what doesn't read is never
// written over.
func TestZeroedCallerKeysReadFromBackup(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	secret, err := Update("add-key", Change{Name: "ci"})
	if err != nil {
		t.Fatal(err)
	}
	// a second write keeps the first store as the .bak (Keep)
	if _, err := Update("add-key", Change{Name: "other"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := Authenticate(secret); !ok {
		t.Fatal("the key just added doesn't work")
	}
	zeroed := make([]byte, 700)
	os.WriteFile(Path(), zeroed, 0o600)
	if _, ok := Authenticate(secret); !ok {
		t.Fatal("a zeroed caller-keys.json read as no key")
	}

	os.Remove(Path() + ".bak")
	if _, err := Update("add-key", Change{Name: "New"}); err == nil {
		t.Fatal("an add over keys that don't read said done")
	}
	if got, _ := os.ReadFile(Path()); !bytes.Equal(got, zeroed) {
		t.Fatal("a change wrote over keys that don't read")
	}
}
