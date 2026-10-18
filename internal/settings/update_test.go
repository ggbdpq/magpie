package settings

import (
	"bytes"
	"errors"
	"os"
	"runtime"
	"testing"
)

// An update reads the file checked: a settings.json that cannot be read is
// unknown, never empty, and the update stops before its callback instead of
// saving the defaults plus the asked-for change over the real one (#2813).
func TestUpdateRefusesUnreadableSettings(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows read restrictions are covered by TestSaveKeepsReadBlockedSettings's handle shape")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"theme":"dark","githubToken":"SYNTHETIC_PRIVATE_TOKEN","allowIPs":["203.0.113.7"]}`)
	if err := os.WriteFile(Path(), original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(Path(), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(Path(), 0o600) })
	if _, err := os.ReadFile(Path()); !errors.Is(err, os.ErrPermission) {
		t.Skipf("this process can read an unreadable file: %v", err)
	}
	ran := false
	err := Update(func(s *Settings) error {
		// the read failed and the callback ran anyway: put the file back so
		// the save goes through, which is how the real settings were lost
		ran = true
		if cerr := os.Chmod(Path(), 0o600); cerr != nil {
			return cerr
		}
		s.NoAutoUpdate = true
		return nil
	})
	if ran {
		t.Error("the callback ran on a file that could not be read")
	}
	if err == nil {
		t.Fatal("Update saved over settings it could not read, and said nothing")
	}
	if err := os.Chmod(Path(), 0o600); err != nil {
		t.Fatal(err)
	}
	if after, err := os.ReadFile(Path()); err != nil || !bytes.Equal(after, original) {
		t.Errorf("the unreadable settings were overwritten: %v", err)
	}
}
