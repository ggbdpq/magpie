package davsync

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// A push never carries a plugin list this computer didn't read (#6545,
// Tomahawkd): a crash can leave plugins.json unreadable with no good
// generation, and the sync ran anyway. Carried as an empty list, the
// computers applying it read it as one removed there, and took every
// plugin away. Carried by no list, nothing is told, and the plugins stay.
func TestSyncPushCarriesNoListItDidntRead(t *testing.T) {
	fake := &fakeDAV{files: map[string][]byte{}, etags: map[string]string{}, dirs: map[string]bool{"/dav": true}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	cfg := Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse"}
	now := func(t *testing.T) {
		t.Helper()
		if err := Now(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	oldAdd, oldRemove := addPlugins, removePlugin
	t.Cleanup(func() { addPlugins, removePlugin = oldAdd, oldRemove })
	addPlugins = func(es []plugin.Entry) ([]string, map[string]string) {
		here := pluginsHere()
		for _, e := range es {
			if !slices.Contains(here, e.Spec) {
				here = append(here, e.Spec)
			}
		}
		setPlugins(t, here...)
		return nil, nil
	}
	removePlugin = func(name string) error {
		setPlugins(t, slices.DeleteFunc(pluginsHere(), func(s string) bool { return plugin.Name(s) == name })...)
		return nil
	}
	const x, y = "@magpie-community/opencode-x-auth", "@magpie-community/opencode-y-auth"
	a, b := newComputer(t), newComputer(t)

	a.use(t)
	setPlugins(t, x, y)
	Configure(cfg)
	now(t)
	b.use(t)
	Configure(cfg)
	now(t)
	if got := pluginsHere(); !slices.Equal(got, []string{x, y}) {
		t.Fatalf("b has %v after joining, want both", got)
	}
	a.use(t)

	// a's plugins.json is left unreadable (no good generation) while a
	// provider changes, so the changed providers part goes out
	if err := os.WriteFile(provider.Path(), []byte(`{"providers":[{"id":"acme","name":"Acme","chat":"https://acme.example.invalid/v1","key":"sk-acme"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settings.Dir(), "plugins.json"), []byte(`{"plugins":`), 0o600); err != nil {
		t.Fatal(err)
	}
	now(t)
	up, err := backup.Open(fake.files["/dav/magpie/magpie.magpie-backup"], "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if up.Plugins != nil {
		t.Fatalf("a pushed the plugin list %+v, though its plugins.json couldn't be read: want no list", *up.Plugins)
	}
	b.use(t)
	now(t)
	if got := pluginsHere(); !slices.Equal(got, []string{x, y}) {
		t.Fatalf("b has %v after a's unreadable list was applied, want both kept", got)
	}
}

// A push carries the plugins installed now, as plugins.json keeps them
// (#6545, Tomahawkd): the list in the bundle was read when the sync
// started, and a plugin installed since — a reinstall that was still
// running then — would go out missing, and the computers applying it would
// take it away as one removed there. A removal this sync is spreading but
// couldn't do here yet stays unsent: x is not sent back in while b still
// tries to take it away.
func TestSyncPushCarriesThePluginsInstalledNow(t *testing.T) {
	fake := &fakeDAV{files: map[string][]byte{}, etags: map[string]string{}, dirs: map[string]bool{"/dav": true}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	cfg := Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse"}
	now := func(t *testing.T) {
		t.Helper()
		if err := Now(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	oldAdd, oldRemove := addPlugins, removePlugin
	t.Cleanup(func() { addPlugins, removePlugin = oldAdd, oldRemove })
	addPlugins = func(es []plugin.Entry) ([]string, map[string]string) {
		here := pluginsHere()
		var added []string
		for _, e := range es {
			if !slices.Contains(here, e.Spec) {
				here = append(here, e.Spec)
				added = append(added, e.Spec)
			}
		}
		setPlugins(t, here...)
		return added, nil
	}
	removePlugin = func(name string) error {
		return errors.New("its agent is on magpie") // stuck: tried again at the next sync
	}
	const x, y, z = "@magpie-community/opencode-x-auth", "@magpie-community/opencode-y-auth", "@magpie-community/opencode-z-auth"
	a, b := newComputer(t), newComputer(t)

	a.use(t)
	setPlugins(t, x, y)
	Configure(cfg)
	now(t)
	b.use(t)
	Configure(cfg)
	now(t)
	if got := pluginsHere(); !slices.Equal(got, []string{x, y}) {
		t.Fatalf("b has %v after joining, want both", got)
	}

	// a takes x away and syncs; b's copy can't be taken away just now
	a.use(t)
	setPlugins(t, y)
	now(t)

	// b installs z while x is stuck, and changes its settings, so the
	// sync takes b's settings and pushes
	b.use(t)
	setPlugins(t, x, y, z)
	settings.Save(settings.Settings{Theme: "light"})
	now(t)
	if !slices.Contains(pluginsHere(), x) {
		t.Fatal("b's x went away, want it kept for the next try")
	}
	up, err := backup.Open(fake.files["/dav/magpie/magpie.magpie-backup"], "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	if up.Plugins != nil {
		for _, e := range *up.Plugins {
			names = append(names, plugin.Name(e.Spec))
		}
	}
	if !slices.Equal(names, []string{plugin.Name(y), plugin.Name(z)}) {
		t.Fatalf("b pushed the plugins %v, want y (x's removal stuck, not sent) and z (installed here)", names)
	}

	// a follows: z reaches it, and x's removal is not undone
	a.use(t)
	now(t)
	if got := pluginsHere(); !slices.Equal(got, []string{y, z}) {
		t.Fatalf("a has %v after b's sync, want y and z, x still away", got)
	}
}

// A sync says what it did to the plugins (#6545, Tomahawkd): one added as
// another computer has it, and one removed as another computer removed it.
// Nothing was logged before, so a plugin gone from a machine left no trace.
func TestSyncLogsPluginChanges(t *testing.T) {
	fake := &fakeDAV{files: map[string][]byte{}, etags: map[string]string{}, dirs: map[string]bool{"/dav": true}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	cfg := Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse"}
	now := func(t *testing.T) {
		t.Helper()
		if err := Now(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	oldAdd, oldRemove := addPlugins, removePlugin
	t.Cleanup(func() { addPlugins, removePlugin = oldAdd, oldRemove })
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	addPlugins = func(es []plugin.Entry) ([]string, map[string]string) {
		here := pluginsHere()
		var added []string
		for _, e := range es {
			if !slices.Contains(here, e.Spec) {
				here = append(here, e.Spec)
				added = append(added, e.Spec)
			}
		}
		setPlugins(t, here...)
		return added, nil
	}
	removePlugin = func(name string) error {
		setPlugins(t, slices.DeleteFunc(pluginsHere(), func(s string) bool { return plugin.Name(s) == name })...)
		return nil
	}
	const x, y = "@magpie-community/opencode-x-auth", "@magpie-community/opencode-y-auth"
	a, b := newComputer(t), newComputer(t)

	a.use(t)
	setPlugins(t, x, y)
	Configure(cfg)
	now(t)
	b.use(t)
	Configure(cfg)
	now(t)
	for _, spec := range []string{x, y} {
		if said := "sync: plugin " + spec + " added, as another computer has it"; !strings.Contains(buf.String(), said) {
			t.Fatalf("b's join said %q, want it to say %q", buf.String(), said)
		}
	}

	a.use(t)
	setPlugins(t, y)
	now(t)
	b.use(t)
	buf.Reset()
	now(t)
	if said := "sync: plugin " + plugin.Name(x) + " removed, as another computer removed it"; !strings.Contains(buf.String(), said) {
		t.Fatalf("b's sync said %q, want it to say %q", buf.String(), said)
	}
}
