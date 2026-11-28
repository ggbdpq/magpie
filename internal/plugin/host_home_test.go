package plugin

import "testing"

// #4296: Windows has no HOME by default. When the plugin host is started
// with HOME empty and USERPROFILE set, plugins that resolve their config as
// "$HOME/.config/magpie" end up with a relative path and write under the
// process' working directory (C:\.config\magpie). The host must pass a
// non-empty HOME so the plugin's two path joins resolve to the user's home.
func TestHostEnvGivesWindowsPluginsAHome(t *testing.T) {
	in := []string{"HOME=", "USERPROFILE=C:\\Users\\tester", "MAGPIE_ADDR=127.0.0.1:3425"}
	got := hostEnv(in)
	const want = `C:\Users\tester`
	found := ""
	for _, e := range got {
		k, v, ok := cutEnvForHostHome(e)
		if ok && k == "HOME" {
			found = v
		}
	}
	if found != want {
		t.Fatalf("plugin host passes HOME=%q, want %q (from USERPROFILE); env: %q", found, want, got)
	}
}

func TestHostEnvKeepsAnExplicitHome(t *testing.T) {
	in := []string{"HOME=/Users/explicit", "USERPROFILE=C:\\Users\\other"}
	got := hostEnv(in)
	count, found := 0, ""
	for _, e := range got {
		k, v, ok := cutEnvForHostHome(e)
		if ok && k == "HOME" {
			count++
			found = v
		}
	}
	if count != 1 || found != "/Users/explicit" {
		t.Fatalf("plugin host HOME entries: %d, value %q; want one /Users/explicit; env: %q", count, found, got)
	}
}

func cutEnvForHostHome(e string) (string, string, bool) {
	for i := 0; i < len(e); i++ {
		if e[i] == '=' {
			return e[:i], e[i+1:], true
		}
	}
	return e, "", false
}
