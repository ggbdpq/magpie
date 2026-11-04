package cli

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// key.protocol= on provider add and set says which one protocol the first
// key works with, as the editor's Key protocol does; any, or empty, takes
// the lock off (#4056: a relay's key picked up for Anthropic alone, its
// URL typed in the OpenAI field, could only be freed by editing
// providers.json by hand). A protocol magpie doesn't know is refused, and
// nothing is written for it. The provider's page says the lock, and how
// to take it off, beside the key.
func TestProviderKeyProtocolPair(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k1", KeyProtocol: provider.Anthropic, Chat: "http://127.0.0.1:1/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := providerCmd([]string{"provider", "set", "relay", "key.protocol=any"}); err != nil {
		t.Fatal(err)
	}
	if p, err := provider.Find("relay"); err != nil || p.KeyProtocol != "" {
		t.Fatalf("any: %q %v", p.KeyProtocol, err)
	}
	for _, c := range []struct {
		v    string
		want provider.Protocol
	}{
		{"anthropic", provider.Anthropic},
		{"chat", provider.Chat},
		{"responses", provider.Responses},
		{"gemini", provider.Gemini},
	} {
		if err := providerCmd([]string{"provider", "set", "relay", "key.protocol=" + c.v}); err != nil {
			t.Fatalf("%q: %v", c.v, err)
		}
		if p, err := provider.Find("relay"); err != nil || p.KeyProtocol != c.want {
			t.Fatalf("key.protocol=%q: %q %v", c.v, p.KeyProtocol, err)
		}
	}
	if err := providerCmd([]string{"provider", "set", "relay", "key.protocol="}); err != nil {
		t.Fatal(err)
	}
	if p, _ := provider.Find("relay"); p.KeyProtocol != "" {
		t.Fatalf("empty left the protocol: %q", p.KeyProtocol)
	}
	if err := providerCmd([]string{"provider", "set", "relay", "key.protocol=openai"}); err == nil || !strings.Contains(err.Error(), "chat|responses|anthropic|gemini|any") {
		t.Fatalf("a made-up protocol: %v", err)
	}
	if p, _ := provider.Find("relay"); p.KeyProtocol != "" {
		t.Fatalf("a refused value changed the protocol: %q", p.KeyProtocol)
	}
}

func showProviderOutput(t *testing.T, args ...string) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	var out strings.Builder
	done := make(chan struct{})
	go func() {
		b, _ := io.ReadAll(r)
		out.Write(b)
		close(done)
	}()
	err = providerCmd(args)
	w.Close()
	os.Stdout = old
	<-done
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestProviderShowsKeyProtocol(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k1", KeyProtocol: provider.Anthropic, Chat: "http://127.0.0.1:1/v1"}); err != nil {
		t.Fatal(err)
	}
	if out := showProviderOutput(t, "provider", "relay"); !strings.Contains(out, "anthropic only") || !strings.Contains(out, "key.protocol=any") {
		t.Fatalf("the key's protocol is said with the way off it: %s", out)
	}
	if err := providerCmd([]string{"provider", "set", "relay", "key.protocol=any"}); err != nil {
		t.Fatal(err)
	}
	if out := showProviderOutput(t, "provider", "relay"); strings.Contains(out, "anthropic only") {
		t.Fatalf("a key for any protocol says no lock: %s", out)
	}
}
