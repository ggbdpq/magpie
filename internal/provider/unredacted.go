package provider

import (
	"net"
	"strings"

	"github.com/yetone/magpie/internal/netproxy"
)

// SkipsRedaction says requests to p go unmasked: the user set Unredacted
// ("Send requests unmasked") and p has somewhere they go. It is the user's
// word for each provider whatever the address (AcMilan on Discord), a
// subscription's and a plugin's too (isee1_1 on Discord: a plugin's
// provider never had the switch): the editor cautions while it is on for
// one not on this computer or the local network, which a signed-in
// account's or a plugin's vendor never is, and nothing here looks one up.
// Another magpie (a Remote magpie) set so is sent the request as written,
// with no X-Magpie-Masked-As, so it masks by its own Settings › Privacy
// and its own providers' switches before anything reaches a vendor
// (CrazyMouse_TT on X: a model of the user's own behind that magpie);
// left off, it is sent the request masked here, as before.
func (p Provider) SkipsRedaction() bool {
	if !p.Unredacted {
		return false
	}
	if p.Account != nil {
		return true // the agent's sign-in or a plugin's: its vendor is where it goes
	}
	for _, b := range []string{p.Chat, p.Responses, p.Anthropic, p.Gemini, p.Decide} {
		if b != "" {
			return true
		}
	}
	return false
}

// LocalServer says p is the user's model at their own computer or on their
// local network: it has an address, and every address it has is a local
// host (LocalHost, so a name of the user's own DNS is looked up). A
// signed-in account and a plugin's provider never are, since their vendor
// is where they go, and neither is another magpie, whose addresses are
// that other computer's and which masks by its own Settings › Privacy.
// Only adding one asks (UnmaskedOnAdd); a request never does.
func (p Provider) LocalServer() bool {
	if p.Account != nil || p.IsRemoteMagpie() {
		return false
	}
	n := 0
	for _, raw := range []string{p.Chat, p.Responses, p.Anthropic, p.Gemini, p.Decide} {
		h := urlHost(raw)
		if h == "" {
			continue
		}
		n++
		if !LocalHost(h) {
			return false
		}
	}
	return n > 0
}

// UnmaskedOnAdd is p as it goes in where the user adds it: their model at
// their own computer or on their local network starts with "Send requests
// unmasked" already on (the owner, 11-18) — their machine, which holds
// whatever they say to it, so masking has nothing there to keep from them,
// and its first request would otherwise be masked for it. Nothing they
// said is changed: the switch already on, and one left off, stand over it
// (the editor sends its own value; `unmasked=no` says so). Only whoever
// adds a provider asks, and only then: a saved provider keeps what it was
// saved with whatever its address is, and a copy, a sync and a gateway all
// go through Add, so nothing is read off an address in Add itself
// (SkipsRedaction is the user's word alone).
func (p Provider) UnmaskedOnAdd() Provider {
	if p.Unredacted || !p.LocalServer() {
		return p
	}
	p.Unredacted = true
	return p
}

// LocalHost says host is on this machine or the local network: localhost,
// a .local name, a loopback, private or link-local IP, or a name every
// address of which is one (netproxy.LocalName, looked up now and kept a
// minute; one that doesn't resolve isn't). Only the editor's caution and
// adding a provider ask it (UnmaskedOnAdd), never a request.
func LocalHost(host string) bool {
	if local(host) {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return netproxy.LocalIP(ip)
	}
	return netproxy.LocalName(host)
}

// urlHost is the host a stored URL is asked at, "" for anything that isn't
// one. A URL saved with no scheme is taken as http, as the editors take
// what is typed for an address, and a name is compared in lower case, as
// the editor's own reading of one does (localIP in app.js).
func urlHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	return strings.ToLower(hostParts(raw).name)
}
