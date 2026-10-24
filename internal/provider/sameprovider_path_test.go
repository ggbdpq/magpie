package provider

import (
	"path/filepath"
	"testing"
)

// #3388: OpenCode Zen and OpenCode Go are two presets of one vendor on one
// host, at different paths (/zen, /zen/go), and one account's key works for
// both. Adding Go beside Zen must not be refused as the one already there;
// the same thing twice still is.
func TestAddSameKeyOtherPathOfHost(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	zen, _ := FromPreset("opencode-zen")
	zen.Key = "sk-opencode"
	if _, err := Add(zen); err != nil {
		t.Fatal(err)
	}
	goprov, _ := FromPreset("opencode-go")
	goprov.Key = "sk-opencode"
	id, err := Add(goprov)
	if err != nil {
		t.Fatalf("Go beside Zen, same key: %v", err)
	}
	got, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Chat != "https://opencode.ai/zen/go/v1" || got.Anthropic != "https://opencode.ai/zen/go" {
		t.Fatalf("Go's endpoints moved: %+v", got)
	}
	// the same preset twice with the same key is still the one already here
	dup, _ := FromPreset("opencode-go")
	dup.Key = "sk-opencode"
	if _, err := Add(dup); err == nil {
		t.Fatal("a duplicate of Go was added")
	}
}

// The dedup's host holds beyond the presets: a custom provider at one host
// with a key of the account there is the one already here, at any path —
// an address of a provider's is its host, as the import list reads it.
func TestAddSameKeySameHostCustom(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if _, err := Add(Provider{Name: "Relay", Chat: "https://relay.example/v1", Key: "sk-1"}); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"https://relay.example/v2", "https://relay.example"} {
		if _, err := Add(Provider{Name: "Relay again", Chat: u, Key: "sk-1"}); err == nil {
			t.Fatalf("%s was added beside the relay", u)
		}
	}
	// another host is another provider, as it always was
	if _, err := Add(Provider{Name: "Elsewhere", Chat: "https://other.example/v1", Key: "sk-1"}); err != nil {
		t.Fatalf("another host: %v", err)
	}
}
