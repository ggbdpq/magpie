package gateway

import (
	"log"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// corsGuard lets the web pages and browser extensions the user listed
// in Settings › Network (settings.CORSOrigins, #1051) call the gateway
// from a browser: their
// preflight is answered, and their calls get the headers that let the page
// read the reply. Such a call carries an enabled gateway key — a page is
// not an agent the user started, and on loopback a call needs no key, so
// without one any script served at that origin (another dev server on
// the same port) could spend the user's subscriptions. A page not listed
// gets no CORS headers, as before, and a request with no Origin (every
// agent's) passes untouched.
//
// Any other web page is refused (403, foreignPage): on loopback a call
// needs no key and the body's Content-Type isn't checked, so a page the
// user merely visits could send a "simple" text/plain POST and spend their
// subscriptions blind. Such a page never read a reply before (no CORS
// headers), so refusing it breaks nothing that worked.
//
// An Origin of "null" is refused too (403) unless the call carries an
// enabled gateway key: any web page can send it from a sandboxed iframe,
// which is that same "simple" POST one frame away (a file:// page and an
// app's webview send it as well). So is a page at a subdomain of
// localhost, other than the two Tauri and Wails serve their webviews at:
// a browser that resolves the name itself keeps such a page on this
// computer, but Safari and other WebKit views ask the configured DNS for
// it (WebKit bug 160504), and a resolver that answers serves it from
// anywhere. A key, as for a listed origin, still lets such a client
// through; neither gets CORS headers, so a frame or a local page still
// sends blind, as before.
func corsGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" || !corsAllowed(origin) {
			if foreignPage(r, origin) {
				log.Printf("refused %s %s from the web page %s: not one listed in Settings", r.Method, r.URL.Path, origin)
				writeError(w, provider.Chat, http.StatusForbidden, "magpie doesn't answer the web page "+origin+": list it in Settings → Network and sharing → Web pages to let it call magpie with a gateway key")
				return
			}
			if (origin == "null" || localhostSubdomain(origin)) && !keyedCaller(r) {
				log.Printf("refused %s %s from %s: a sandboxed frame, a local file, or a page a resolver may serve from anywhere, with no gateway key", r.Method, r.URL.Path, origin)
				writeError(w, provider.Chat, http.StatusForbidden, "magpie doesn't answer a sandboxed frame, a local page (Origin: null) or a page at a localhost subdomain without a gateway key: create one in magpie's Gateway page and send it as Authorization: Bearer <key> (or x-api-key)")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", origin)
		h.Add("Access-Control-Expose-Headers", SessionHeader)
		h.Add("Vary", "Origin")
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			if asked := corsHeaders(r.Header.Get("Access-Control-Request-Headers")); asked != "" {
				h.Set("Access-Control-Allow-Headers", asked)
			}
			// Chrome asks before a public page reaches this computer
			if r.Header.Get("Access-Control-Request-Private-Network") == "true" {
				h.Set("Access-Control-Allow-Private-Network", "true")
			}
			h.Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// lanGuard, in front of Handler on the real server, has already
		// taken the key and put magpie's own token in its place
		if !keyedCaller(r) {
			writeError(w, provider.Chat, http.StatusUnauthorized, "a web page or browser extension calls magpie with a gateway key: create one in magpie's Gateway page and send it as Authorization: Bearer <key> (or x-api-key)")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// keyedCaller: the request carries an enabled gateway key — the caller
// lanGuard recorded, or one in its own headers, which is where a handler
// called without lanGuard finds it (the web app's embedded calls, tests).
func keyedCaller(r *http.Request) bool {
	return access.Caller(r.Context()).KeyID != "" ||
		slices.ContainsFunc(callerKeys(r), func(k string) bool { _, ok := access.Authenticate(k); return ok })
}

// localhostSubdomain: origin is an http(s) page at a subdomain of
// localhost other than the two an installed app's webview serves itself
// at on Windows (http://tauri.localhost, http://wails.localhost). Chrome
// and Firefox resolve *.localhost to loopback by a rule of their own, so
// there such a page is this computer's; Safari and other WebKit views ask
// the configured DNS for it (WebKit bug 160504, unfixed through macOS 15;
// macOS 26 resolves it below WebKit), and a resolver that answers — a
// captive portal's, a network's, an ISP's — serves the page from
// anywhere. corsGuard holds it to a gateway key, as "null": whichever
// browser holds it, a page that may be elsewhere pays its way.
func localhostSubdomain(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if s := strings.ToLower(u.Scheme); s != "http" && s != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return strings.HasSuffix(host, ".localhost") &&
		host != "tauri.localhost" && host != "wails.localhost"
}

// foreignPage: origin is an http(s) web page that is neither on this
// computer (localhost, *.localhost, 127.0.0.0/8, [::1], any port) nor the
// request's own origin (a page served at the address the request names,
// such as magpie's web app on a LAN address), so a browser sent it for a
// site elsewhere. usemagpie.ai may still read the Omarchy theme
// (omarchyTheme).
//
// Only http and https are judged. A scheme only an app or an extension
// can send — app://, file://, tauri://, vscode-webview://,
// chrome-extension:// — comes from desktop apps, editor webviews and
// browser extensions that don't enforce CORS (a webview with web security
// off, an extension holding host permissions), and no ordinary web page
// can put one on a request, so those pass as before. "null" is not one of
// them: any web page sends it from a sandboxed frame and a local file
// sends it too, which corsGuard judges (a gateway key, or 403) — and so
// is a subdomain of localhost another app's name among them
// (localhostSubdomain). Tauri's and Wails' webviews on Windows are
// http://tauri.localhost and http://wails.localhost, whose HTTP plugins
// send that Origin outside CORS; *.localhost counts as this computer for
// the browsers that resolve it by their own rule, and corsGuard asks a
// key of it for the ones that ask the DNS instead.
func foreignPage(r *http.Request, origin string) bool {
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		// a browser's always parses; an unreadable http one is no page of ours
		return strings.HasPrefix(strings.ToLower(origin), "http")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return false
	}
	if hostPort(u.Host, scheme) == hostPort(r.Host, scheme) {
		return false
	}
	if r.URL.Path == "/v1/magpie/omarchy" && siteOrigin(origin) {
		return false
	}
	return true
}

// hostPort is host[:port] lower-cased with the scheme's port filled in, so
// https://a.example and a Host of a.example:443 compare equal.
func hostPort(hp, scheme string) string {
	hp = strings.ToLower(hp)
	if hp == "" {
		return ""
	}
	if h, p, err := net.SplitHostPort(hp); err == nil {
		return net.JoinHostPort(strings.Trim(h, "[]"), p)
	}
	port := "80"
	if scheme == "https" {
		port = "443"
	}
	return net.JoinHostPort(strings.Trim(hp, "[]"), port)
}

// corsAllowed: origin is one the user listed.
func corsAllowed(origin string) bool {
	saved := settings.Load().CORSOrigins
	if len(saved) == 0 {
		return false
	}
	// as saved from Settings, or as written into settings.json by hand
	list, _ := settings.CleanOrigins(saved)
	o, err := settings.CleanOrigins([]string{origin})
	return err == nil && len(o) == 1 && slices.Contains(list, o[0])
}

// corsHeaders is a preflight's Access-Control-Request-Headers as the
// answer allows them: the header names asked for, nothing else.
func corsHeaders(asked string) string {
	var out []string
	for _, f := range strings.Split(asked, ",") {
		f = strings.TrimSpace(f)
		if f != "" && !strings.ContainsFunc(f, func(c rune) bool {
			return !(c == '-' || c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z')
		}) {
			out = append(out, f)
		}
	}
	return strings.Join(out, ", ")
}
