package shortcut

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// A shortcut is made, left be while it opens this exe, and pointed at the
// exe again once that was moved.
func TestWrite(t *testing.T) {
	dir := t.TempDir()
	lnk := filepath.Join(dir, Name+".lnk")
	exe := filepath.Join(dir, "a", "magpie.exe")
	moved := filepath.Join(dir, "b", "magpie.exe")
	for _, f := range []string{exe, moved} {
		os.MkdirAll(filepath.Dir(f), 0o755)
		os.WriteFile(f, []byte("MZ"), 0o755)
	}
	for i, c := range []struct {
		exe     string
		written bool
	}{{exe, true}, {exe, false}, {moved, true}, {moved, false}} {
		written, err := write(lnk, c.exe)
		if err != nil {
			t.Fatal(i, err)
		}
		if written != c.written {
			t.Fatalf("%d: written = %v, want %v", i, written, c.written)
		}
	}
	if _, err := os.Stat(lnk); err != nil {
		t.Fatal("no shortcut:", err)
	}
}

// magpie is in Installed apps (Jevik_AI on X deleted it by hand, which
// runs no code, and Codex kept pointing at a gateway that was gone): the
// entry opens this exe's Uninstall, follows an exe that moved, goes with
// Remove, and one another magpie's exe still holds stays.
func TestInstalledAppsEntry(t *testing.T) {
	was, wasApp := uninstallKey, appPath
	uninstallKey = `Software\magpie-test\` + t.Name() + `\Uninstall\magpie`
	appPath = `Software\magpie-test\` + t.Name() + `\App Paths\magpie.exe`
	t.Cleanup(func() {
		registry.DeleteKey(registry.CURRENT_USER, uninstallKey)
		registry.DeleteKey(registry.CURRENT_USER, appPath)
		registry.DeleteKey(registry.CURRENT_USER, `Software\magpie-test\`+t.Name()+`\Uninstall`)
		registry.DeleteKey(registry.CURRENT_USER, `Software\magpie-test\`+t.Name()+`\App Paths`)
		registry.DeleteKey(registry.CURRENT_USER, `Software\magpie-test\`+t.Name())
		uninstallKey, appPath = was, wasApp
	})
	read := func(name string) string {
		k, err := registry.OpenKey(registry.CURRENT_USER, uninstallKey, registry.QUERY_VALUE)
		if err != nil {
			return ""
		}
		defer k.Close()
		v, _, _ := k.GetStringValue(name)
		return v
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "a", "magpie.exe")
	other := filepath.Join(dir, "b", "magpie.exe")
	for _, f := range []string{exe, other} {
		os.MkdirAll(filepath.Dir(f), 0o755)
		os.WriteFile(f, []byte("MZ"), 0o755)
	}
	// written where every installed start writes App Paths
	registerAppPath(exe)
	if got := read("UninstallString"); got != UninstallCommand(exe) {
		t.Fatalf("UninstallString = %q", got)
	}
	if read("DisplayName") != Name || read("InstallLocation") != filepath.Dir(exe) || read("DisplayIcon") != exe+",0" {
		t.Fatalf("entry: %q %q %q", read("DisplayName"), read("InstallLocation"), read("DisplayIcon"))
	}
	// another magpie, still there, keeps its entry
	if err := unregisterAppPath(other); err != nil {
		t.Fatal(err)
	}
	if read("UninstallString") == "" {
		t.Fatal("removed another magpie's entry")
	}
	if err := unregisterAppPath(exe); err != nil {
		t.Fatal(err)
	}
	if read("UninstallString") != "" {
		t.Fatal("the entry stayed after Remove")
	}
	// one whose exe is gone goes with any magpie's uninstall
	registerUninstall(other)
	os.Remove(other)
	if err := unregisterUninstall(exe); err != nil {
		t.Fatal(err)
	}
	if read("UninstallString") != "" {
		t.Fatal("an entry for an exe that is gone stayed")
	}
}

// H3nryM4 (#6929): every build before wrote its entry under the bare
// "magpie" name, which a winget manifest for another product (newdee.
// Magpie) declares as its ProductCode — winget read magpie's entry as
// that product's, version unknown. The entry lives under magpie's own
// name now; one of magpie's under the shared name moves off it at the
// next start, another product's stays, and an uninstall takes both names.
func TestUninstallEntryMovesOffTheSharedName(t *testing.T) {
	was, wasOld, wasApp := uninstallKey, oldUninstallKey, appPath
	uninstallKey = `Software\magpie-test\` + t.Name() + `\Uninstall\yetone.magpie`
	oldUninstallKey = `Software\magpie-test\` + t.Name() + `\Uninstall\magpie`
	appPath = `Software\magpie-test\` + t.Name() + `\App Paths\magpie.exe`
	t.Cleanup(func() {
		for _, k := range []string{uninstallKey, oldUninstallKey, appPath, `Software\magpie-test\` + t.Name()} {
			registry.DeleteKey(registry.CURRENT_USER, k)
		}
		uninstallKey, oldUninstallKey, appPath = was, wasOld, wasApp
	})
	read := func(name string) string {
		k, err := registry.OpenKey(registry.CURRENT_USER, uninstallKey, registry.QUERY_VALUE)
		if err != nil {
			return ""
		}
		defer k.Close()
		v, _, _ := k.GetStringValue(name)
		return v
	}
	sharedHas := func() bool {
		k, err := registry.OpenKey(registry.CURRENT_USER, oldUninstallKey, registry.QUERY_VALUE)
		if err != nil {
			return false
		}
		k.Close()
		return true
	}
	setShared := func(exe string) {
		k, _, err := registry.CreateKey(registry.CURRENT_USER, oldUninstallKey, registry.SET_VALUE)
		if err != nil {
			t.Fatal(err)
		}
		defer k.Close()
		_ = k.SetStringValue("DisplayIcon", exe+",0")
		_ = k.SetStringValue("DisplayName", Name)
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "a", "magpie.exe")
	other := filepath.Join(dir, "b", "magpie.exe")
	for _, f := range []string{exe, other} {
		os.MkdirAll(filepath.Dir(f), 0o755)
		os.WriteFile(f, []byte("MZ"), 0o755)
	}

	// the other product borrowed the shared name: its entry points at its
	// own exe, and magpie's start leaves it there
	setShared(other)
	registerUninstall(exe)
	if read("DisplayName") != Name {
		t.Fatalf("the entry under magpie's own name wasn't written")
	}
	if !sharedHas() {
		t.Fatal("another product's entry was taken")
	}

	// a shared-name entry that is magpie's moves off at the next start
	setShared(exe)
	registerUninstall(exe)
	if sharedHas() {
		t.Fatal("magpie's entry stayed under the shared name")
	}
	if read("UninstallString") != UninstallCommand(exe) {
		t.Fatalf("the entry under magpie's own name was disturbed")
	}

	// an uninstall takes both names, another magpie's exe still holding a
	// shared-name entry excepted
	setShared(other)
	if err := unregisterUninstall(exe); err != nil {
		t.Fatal(err)
	}
	if !sharedHas() {
		t.Fatal("another product's shared-name entry was taken by magpie's uninstall")
	}
	setShared(exe)
	os.Remove(exe)
	if err := unregisterUninstall(exe); err != nil {
		t.Fatal(err)
	}
	if sharedHas() {
		t.Fatal("a shared-name entry for an exe that is gone stayed")
	}
	if read("UninstallString") != "" {
		t.Fatal("magpie's own entry stayed after Remove")
	}
}
