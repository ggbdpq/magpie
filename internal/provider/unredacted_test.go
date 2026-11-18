package provider

import (
	"context"
	"net"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/netproxy"
)

// "Send requests unmasked" is the user's switch for each provider
// (AcMilan on Discord): set, a provider goes unmasked at whatever address,
// a name of the user's own DNS or a vendor's; unset, it is masked. A
// signed-in account and a plugin's provider take it too (isee1_1), and so
// does another magpie, which then masks by its own settings
// (CrazyMouse_TT on X). Nothing is
// looked up to decide: the lookup fails the test if it is.
func TestSkipsRedaction(t *testing.T) {
	t.Cleanup(netproxy.SetLookup(func(_ context.Context, host string) ([]net.IPAddr, error) {
		t.Errorf("SkipsRedaction looked up %s", host)
		return nil, &net.DNSError{Err: "no lookups here", Name: host}
	}))
	cases := []struct {
		name string
		p    Provider
		want bool
	}{
		{"ollama", Provider{Unredacted: true, Chat: "http://localhost:11434/v1", Anthropic: "http://localhost:11434"}, true},
		{"lan", Provider{Unredacted: true, Chat: "http://192.168.1.20:8000/v1"}, true},
		{"a lan name", Provider{Unredacted: true, Chat: "http://llm.corp.lan:8000/v1"}, true},
		{"a lan name, off", Provider{Chat: "http://llm.corp.lan:8000/v1"}, false},
		{"a vendor, set by the user", Provider{Unredacted: true, Chat: "https://api.deepseek.com/v1"}, true},
		{"a public ip, set by the user", Provider{Unredacted: true, Chat: "http://8.8.8.8/v1"}, true},
		{"through its own proxy", Provider{Unredacted: true, Chat: "http://llm.corp.lan:8000/v1", Proxy: "socks5://127.0.0.1:1080"}, true},
		{"not set", Provider{Chat: "http://localhost:11434/v1"}, false},
		{"no address", Provider{Unredacted: true}, false},
		{"an account", Provider{Unredacted: true, Account: &Account{Agent: "codex"}}, true},
		{"an account, off", Provider{Account: &Account{Agent: "codex"}}, false},
		{"a plugin's provider", Provider{Unredacted: true, Chat: "plugin://translate", Account: &Account{Agent: "translate"}}, true},
		{"another magpie, which masks by its own settings", Provider{Unredacted: true, Preset: RemoteMagpiePreset, Anthropic: "http://192.168.1.5:3425"}, true},
		{"another magpie, left masked", Provider{Preset: RemoteMagpiePreset, Anthropic: "http://192.168.1.5:3425"}, false},
	}
	for _, tc := range cases {
		if got := tc.p.SkipsRedaction(); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// LocalHost, which decides only whether the editor cautions, takes
// localhost, .local, loopback, private and link-local IPs, and a name
// that resolves only to those; a public name or IP, one resolving to a
// public address among private ones, or one that doesn't resolve, isn't.
func TestLocalHost(t *testing.T) {
	fakeDNS(t, map[string][]string{
		"llm.corp.lan":   {"192.168.1.20"},
		"gpu.home.arpa":  {"10.0.0.7", "fd00::7"},
		"public.example": {"203.0.113.7"},
		"mixed.example":  {"10.0.0.2", "203.0.113.7"},
	})
	for host, want := range map[string]bool{
		"localhost": true, "gpu-box.local": true, "127.0.0.1": true, "::1": true, "192.168.1.20": true, "169.254.10.2": true,
		"llm.corp.lan": true, "gpu.home.arpa": true,
		"public.example": false, "mixed.example": false, "gone.lan": false, "8.8.8.8": false,
	} {
		if got := LocalHost(host); got != want {
			t.Errorf("%s: %v, want %v", host, got, want)
		}
	}
}

// LocalServer, asked only of a provider being added (UnmaskedOnAdd), reads
// its addresses the way the editor does: the user's own model at their own
// computer or on their local network, at every address it has. A name that
// doesn't resolve and a vendor's aren't, so nothing is asked on the network
// here either.
func TestLocalServer(t *testing.T) {
	fakeDNS(t, map[string][]string{
		"llm.corp.lan":   {"192.168.1.20"},
		"vendor.example": {"203.0.113.7"},
	})
	cases := []struct {
		name string
		p    Provider
		want bool
	}{
		{"loopback", Provider{Name: "My box", Chat: "http://127.0.0.1:8080/v1"}, true},
		{"no scheme saved", Provider{Name: "My box", Chat: "127.0.0.1:8080/v1"}, true},
		{"localhost, as the local presets have it", Provider{Name: "Ollama", Preset: "ollama", Chat: "http://localhost:11434/v1", Anthropic: "http://localhost:11434"}, true},
		{"on the LAN", Provider{Name: "vLLM box", Chat: "http://192.168.1.20:8000/v1"}, true},
		{"https on the LAN", Provider{Name: "Lab", Responses: "https://10.0.0.4:8443/v1"}, true},
		{"a LAN name", Provider{Name: "Corp LLM", Chat: "http://llm.corp.lan:8000/v1"}, true},
		{"a vendor", Provider{Name: "DeepSeek", Chat: "https://api.deepseek.com/v1"}, false},
		{"a public IP", Provider{Name: "VPS", Chat: "http://203.0.113.7:8000/v1"}, false},
		{"a name that doesn't resolve", Provider{Name: "Typo", Chat: "http://llm.corp.lanx:8000/v1"}, false},
		{"local and a vendor among them", Provider{Name: "Relay", Chat: "http://127.0.0.1:8080/v1", Anthropic: "https://vendor.example"}, false},
		{"no address", Provider{Name: "Nothing"}, false},
		{"a signed-in account at a local address", Provider{Name: "Codex", Chat: "http://127.0.0.1:4321/v1", Account: &Account{Agent: "codex"}}, false},
		{"another magpie on the LAN", Provider{Name: "Office", Preset: RemoteMagpiePreset, Anthropic: "http://192.168.1.5:3425"}, false},
	}
	for _, tc := range cases {
		if got := tc.p.LocalServer(); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// UnmaskedOnAdd, which whoever adds a provider by command asks of it (the
// editor reads the same thing for its own switch): a local server goes in
// with the switch on and nothing else about it changes. What the user said
// stands over it — a vendor they set anyway keeps the switch — and one with
// nothing local about it goes in as it came. Leaving a local one masked is
// the adder's to say before asking: the CLI's unmasked=no and the editor's
// switch left off never come here at all.
func TestUnmaskedOnAdd(t *testing.T) {
	fakeDNS(t, map[string][]string{"llm.corp.lan": {"192.168.1.20"}})
	cases := []struct {
		name string
		p    Provider
		want bool
	}{
		{"a box on this computer", Provider{Name: "My box", Chat: "http://127.0.0.1:8080/v1"}, true},
		{"a local preset", Provider{Name: "Ollama", Preset: "ollama", Chat: "http://localhost:11434/v1"}, true},
		{"a box on the LAN", Provider{Name: "vLLM box", Chat: "http://192.168.1.20:8000/v1"}, true},
		{"a LAN name", Provider{Name: "Corp LLM", Chat: "http://llm.corp.lan:8000/v1"}, true},
		{"one the user already set", Provider{Name: "My box", Chat: "http://127.0.0.1:8080/v1", Unredacted: true}, true},
		{"a vendor, left masked", Provider{Name: "DeepSeek", Chat: "https://api.deepseek.com/v1"}, false},
		{"a vendor the user set anyway", Provider{Name: "Vendor", Chat: "https://api.deepseek.com/v1", Unredacted: true}, true},
		{"no address", Provider{Name: "Nothing"}, false},
		{"a signed-in account", Provider{Name: "Codex", Chat: "http://127.0.0.1:4321/v1", Account: &Account{Agent: "codex"}}, false},
		{"another magpie on the LAN", Provider{Name: "Office", Preset: RemoteMagpiePreset, Anthropic: "http://192.168.1.5:3425"}, false},
	}
	for _, tc := range cases {
		got := tc.p.UnmaskedOnAdd()
		if got.Unredacted != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got.Unredacted, tc.want)
		}
		if got.Chat != tc.p.Chat || got.Name != tc.p.Name || got.Preset != tc.p.Preset {
			t.Errorf("%s: the provider itself changed: %+v", tc.name, got)
		}
	}
}

// Add reads no address: what a saved provider does is only what it was
// saved with, so a copy, a sync, and everything the gateway adds keep the
// switch as it was given (UnmaskedOnAdd is the adder's to ask).
func TestAddReadsNoAddress(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	id, err := Add(Provider{Name: "Box", Chat: "http://127.0.0.1:8000/v1", Key: "sk-box", Models: []string{"m"}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	if p.Unredacted {
		t.Fatal("Add turned the switch on from the address: only whoever adds the provider asks UnmaskedOnAdd")
	}
	if !p.LocalServer() {
		t.Fatal("the box at 127.0.0.1 isn't a local server")
	}
}

// fakeDNS has names resolve to m's addresses and no other name resolve,
// so no test looks a name up on the network.
func fakeDNS(t *testing.T, m map[string][]string) {
	t.Helper()
	t.Cleanup(netproxy.SetLookup(func(_ context.Context, host string) ([]net.IPAddr, error) {
		ips, ok := m[host]
		if !ok {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		var out []net.IPAddr
		for _, s := range ips {
			out = append(out, net.IPAddr{IP: net.ParseIP(s)})
		}
		return out, nil
	}))
}
