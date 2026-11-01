package provider

import (
	"bytes"
	"os"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// Setting a name to what it already is stays a no-op: nothing is written,
// so a settings.json that cannot be written leaves the setter silent
// (#2813 — the update path saved the file's own contents back, and a
// read-only file answered permission denied where the parent said nil).
func TestSetModelNameUnchangedStaysNoOp(t *testing.T) {
	prefsHome(t)
	if err := SetModelName("a/sol", "My Sol"); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(settings.Path())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(settings.Path(), 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(settings.Path(), 0o600) })
	if _, err := os.OpenFile(settings.Path(), os.O_WRONLY, 0o600); err == nil {
		t.Skip("this process can write a read-only settings file")
	}
	changed, err := setModelName("a/sol", "My Sol")
	if err != nil {
		t.Fatalf("setting the same name on a read-only settings file: %v", err)
	}
	if changed {
		t.Error("setting the same name reported a change")
	}
	if after, err := os.ReadFile(settings.Path()); err != nil || !bytes.Equal(after, original) {
		t.Errorf("the no-op changed the file: %v", err)
	}
	// an update that finds the file already as wanted is silent on a file
	// that cannot be written, and one that changes it still is not
	if err := os.Chmod(settings.Path(), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := setModelName("a/sol", "Other Sol"); err != nil {
		t.Fatalf("setting a new name: %v", err)
	}
	if got := settings.Load().ModelNames["a/sol"]; got != "Other Sol" {
		t.Fatalf("settings hold %q", got)
	}
}
