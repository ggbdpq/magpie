package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/settings"
)

// Zed has no plugin of bili's. Its README lists it with two routes: the
// launcher (bili zed), Linux only and leaving a provider on loopback
// direct, so magpie's gateway never passes through it; and, on every
// system, the /bili/ prefix its docs give for any client whose model
// address can be edited ("Adopting unlisted clients", docs/clients.md):
// language_models.openai_compatible.<provider>.api_url as
// http://127.0.0.1:<port>/bili/<the address>, with bili's daemon (bili
// start) answering there. That is what magpie writes into Zed's magpie
// provider (agent.ZedThrough, which keeps it across picks and syncs and
// saves the key for it, Zed looking that up by the address). magpie keeps
// a bili start of its own running while such an agent is on (keepBiliServe,
// from KeepLinked), ended with magpie; one the user started is used as it
// is. The port is the one bili start itself would bind (biliListenPort):
// ACP_PORT, then PORT, then "port" in billion-context.json, otherwise
// 8787. A foreign listener on the default 8787 (a 401, not bili's health)
// is not that daemon (#7382).

// biliDefaultPort is bili start's port when nothing else names one
// (src/config.ts, fileConfig.port ?? 8787). bili's lanes take
// ZONE_PORT_BASE, 18787, and up.
const biliDefaultPort = 8787

// biliZonePort is the first port bili keeps for a lane (ZONE_PORT_BASE
// in src/config.ts). When the default 8787 answers as something other
// than bili, the shared daemon uses a bili already there, or the first
// port from here that is free.
const biliZonePort = 18787

// biliPortSpan is how many ports past biliZonePort are tried. A lane
// that drifted a long way is still found from its registry file first.
const biliPortSpan = 16

// biliPrefix is the /bili/ prefix on the default port. Tests and a
// machine with no other port still write this. The prefix actually
// written is biliPrefixNow.
const biliPrefix = "http://127.0.0.1:8787/bili/"

// biliURLLane is an agent bili folds through its /bili/ prefix: whether
// its magpie provider goes through it, and putting it there (prefix
// biliPrefix) or back on the gateway ("").
type biliURLLane struct {
	on  func(a *agent.Agent) bool
	set func(a *agent.Agent, prefix string) error
}

// zedThrough is agent.ZedThrough; a var for tests, which save no key.
var zedThrough = agent.ZedThrough

// Hermes has a plugin of bili's (bili plugin install hermes), but it
// routes Hermes's traffic by setting HTTPS_PROXY in Hermes's process
// alone (hermes-plugin/__init__.py apply_env), and magpie's provider is at
// http://127.0.0.1, which that never takes: the plugin would add its
// tools while nothing folds. So Hermes goes through the /bili/ prefix as
// Zed does: agent.HermesThrough puts the base_url of magpie's entries, in
// every profile that has them, behind it, which a sync and a pick keep
// (Duckb7si on X).

var biliURLLanes = map[string]biliURLLane{
	"zed": {
		on:  func(a *agent.Agent) bool { return biliThroughURL(agent.ZedURL(a)) },
		set: func(a *agent.Agent, prefix string) error { return zedThrough(a, prefix) },
	},
	"hermes": {
		on:  func(a *agent.Agent) bool { return biliThroughURL(agent.HermesURL(a)) },
		set: agent.HermesThrough,
	},
	// dsh's own plugin for bili (dsh plugin add billion-context) rides proxy
	// settings of dsh's process, which magpie's http://127.0.0.1 route never
	// takes; its route's baseURL is editable, so it goes through the prefix
	// as Hermes does, in every profile (VantH_off on X).
	"dsh": {
		on:  func(a *agent.Agent) bool { return biliThroughURL(agent.DshURL(a)) },
		set: agent.DshThrough,
	},
}

// biliThroughURL says url is a magpie provider put behind bili's prefix
// on loopback. The port varies (biliPrefixNow); 8787 is only the default.
func biliThroughURL(url string) bool {
	rest, ok := strings.CutPrefix(url, "http://127.0.0.1:")
	if !ok {
		return false
	}
	i := strings.Index(rest, "/bili/")
	if i <= 0 {
		return false
	}
	n, err := strconv.Atoi(rest[:i])
	return err == nil && n >= 1 && n <= 65535
}

// biliHeard is what a probe of /__bili/health found.
type biliHeard int

const (
	biliDown     biliHeard = iota // nothing accepted the connection
	biliUp                        // {"ok":true}
	biliStarting                  // 503 {"ok":false,"reason":"starting"} — bound, not registered yet
	biliStalled                   // TCP accepted, no HTTP answer (slow boot, non-HTTP listener, drop)
	biliOther                     // HTTP answer that is not bili's health
)

// biliDialTimeout is how long a health probe waits to establish TCP.
// Windows retries loopback RSTs for ~2s before reporting WSAECONNREFUSED,
// so dialing gets its own budget separate from biliProbeTimeout.
var biliDialTimeout = 3 * time.Second

// biliProbeTimeout is how long a health probe waits for an HTTP reply
// after TCP has connected. A stall past this is a listener, not a free port.
var biliProbeTimeout = 2 * time.Second

// biliDial establishes the TCP connection for biliProbe. A var for tests.
var biliDial = func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := net.Dialer{Timeout: biliDialTimeout}
	return d.DialContext(ctx, network, addr)
}

// biliProbe asks origin whether bili is the thing listening. A proxy in
// the environment is not asked: the daemon is on this computer, and a
// proxy would hide a 401 from the program that owns the port (#7382).
// Any TCP connect failure (refused, or a dial timeout) is biliDown. Once
// TCP accepts, a disconnect or stall before a complete HTTP reply within
// biliProbeTimeout is biliStalled: occupied during pre-spawn probes, but
// waited out while our own newly started child is still alive. A non-bili
// HTTP response (e.g. 401) is biliOther. 503 {"ok":false,"reason":"starting"}
// is bili itself, bound before registration finishes (src/server/admin.ts);
// it is not another program.
func biliProbe(origin string) biliHeard {
	var connected atomic.Bool
	c := &http.Client{
		Transport: &http.Transport{
			Proxy:                 nil,
			DisableKeepAlives:     true,
			ResponseHeaderTimeout: biliProbeTimeout,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				conn, err := biliDial(ctx, network, addr)
				if err != nil {
					return nil, err
				}
				connected.Store(true)
				_ = conn.SetDeadline(time.Now().Add(biliProbeTimeout))
				return conn, nil
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	res, err := c.Get(origin + "/__bili/health")
	if err != nil {
		if !connected.Load() {
			return biliDown
		}
		return biliStalled
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 4096))
	if err != nil {
		return biliStalled
	}
	var h struct {
		OK     bool   `json:"ok"`
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(body, &h)
	if res.StatusCode == http.StatusServiceUnavailable && !h.OK && h.Reason == "starting" {
		return biliStarting
	}
	if res.StatusCode == http.StatusOK && h.OK {
		return biliUp
	}
	return biliOther
}

func biliOriginOf(port int) string { return "http://127.0.0.1:" + strconv.Itoa(port) }

// biliPortNow is the origin the shared daemon last resolved, empty until
// then. The prefix tests compare against stays biliPrefix until a run
// picks another port.
var biliPortNow struct {
	sync.Mutex
	origin string
}

func biliRemember(origin string) {
	biliPortNow.Lock()
	biliPortNow.origin = strings.TrimSuffix(origin, "/")
	biliPortNow.Unlock()
}

func biliPrefixNow() string {
	biliPortNow.Lock()
	defer biliPortNow.Unlock()
	if biliPortNow.origin != "" {
		return biliPortNow.origin + "/bili/"
	}
	return biliPrefix
}

// resetBiliListen stops magpie's bili and forgets the port it resolved.
// Tests use it so a later test still sees the default prefix.
func resetBiliListen() {
	biliStop()
	biliPortNow.Lock()
	biliPortNow.origin = ""
	biliPortNow.Unlock()
}

// biliListenPort is the port `bili start` binds when magpie does not pass
// --port (src/config.ts loadOptions): ACP_PORT, then PORT, then the
// file's port, then 8787, joined with ?? and parseInt. userSet is false
// only for that default, including a file port of null. A variable that
// is set, even to "", is used and does not fall through. A file that can't
// be read or isn't JSON falls back to 8787, matching bili's loadConfigFile
// returning {}. A named BILI_CONFIG_FILE whose file is missing is an error,
// as bili start's checkNamedConfigRef exits before it listens.
func biliListenPort() (port int, userSet bool, err error) {
	if p, ok, err := biliEnvPort("ACP_PORT"); ok || err != nil {
		return p, true, err
	}
	if p, ok, err := biliEnvPort("PORT"); ok || err != nil {
		return p, true, err
	}
	return biliFilePort()
}

func biliEnvPort(name string) (int, bool, error) {
	s, ok := os.LookupEnv(name)
	if !ok {
		return 0, false, nil
	}
	n, num := biliJSParseInt(s)
	if !num || n < 1 || n > 65535 {
		return 0, true, fmt.Errorf("bili's %s is %q, which isn't a port from 1 to 65535", name, s)
	}
	return n, true, nil
}

func biliFilePort() (int, bool, error) {
	p := BiliConfigFile()
	if ref, ok := biliConfigRef(); ok {
		if _, named := resolveBiliConfigRef(ref); named {
			if _, err := os.Stat(p); os.IsNotExist(err) {
				return 0, true, biliNamedConfigError(ref, p)
			}
		}
	}
	text, ok, err := biliText(p)
	if err != nil {
		log.Printf("library: billion-context: ignoring unreadable config %s: %v", p, err)
		return biliDefaultPort, false, nil
	}
	if !ok || strings.TrimSpace(text) == "" {
		return biliDefaultPort, false, nil
	}
	r, parsed := biliParse(text)
	if !parsed {
		log.Printf("library: billion-context: ignoring unparseable config %s", p)
		return biliDefaultPort, false, nil
	}
	v := r.Get("port")
	// missing, or JSON null: fileConfig.port ?? 8787
	if !v.Exists() || v.Type == gjson.Null {
		return biliDefaultPort, false, nil
	}
	s, raw := biliPortText(v)
	n, num := biliJSParseInt(s)
	if !num || n < 1 || n > 65535 {
		if raw == "" {
			raw = s
		}
		return 0, true, fmt.Errorf("billion-context.json port %s isn't a port from 1 to 65535", raw)
	}
	return n, true, nil
}

// biliPortText is the text parseInt sees for a file port: a JSON number
// the way JS stringifies it (`${8787.5}` is "8787.5"), or a string as stored.
func biliPortText(v gjson.Result) (s, raw string) {
	switch v.Type {
	case gjson.Number:
		return strconv.FormatFloat(v.Float(), 'f', -1, 64), strings.TrimSpace(v.Raw)
	case gjson.String:
		return v.String(), v.String()
	default:
		raw = strings.TrimSpace(v.Raw)
		if raw == "" {
			raw = v.String()
		}
		return raw, raw
	}
}

// biliJSParseInt is JS parseInt(s, 10): leading ASCII whitespace is
// skipped, an optional sign, then digits, and the rest is ignored.
// "8787.5" is 8787. num is false when there is no digit (NaN), which an
// empty ACP_PORT or PORT is.
func biliJSParseInt(s string) (n int, num bool) {
	i := 0
	for i < len(s) {
		switch s[i] {
		case ' ', '\t', '\n', '\r', '\v', '\f':
			i++
			continue
		}
		break
	}
	if i >= len(s) {
		return 0, false
	}
	sign := 1
	if s[i] == '+' || s[i] == '-' {
		if s[i] == '-' {
			sign = -1
		}
		i++
	}
	if i >= len(s) || s[i] < '0' || s[i] > '9' {
		return 0, false
	}
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		n = n*10 + int(s[i]-'0')
		if n > 1_000_000_000 {
			return 0, false
		}
		i++
	}
	return sign * n, true
}

// biliTarget is where the shared daemon should answer. up means a bili
// is already there. stick means the user chose the port (the file or the
// environment). A fallback off a foreign default is not sticky: a bind
// conflict there tries the next free lane port.
func biliTarget() (origin string, port int, up, stick bool, err error) {
	return biliTargetSkip(nil)
}

func biliTargetSkip(failed map[int]bool) (origin string, port int, up, stick bool, err error) {
	port, userSet, err := biliListenPort()
	if err != nil {
		return "", 0, false, false, err
	}
	origin = biliOriginOf(port)
	if !failed[port] {
		switch biliClassify(origin) {
		case biliUp:
			return origin, port, true, true, nil
		case biliOther, biliStalled:
			if userSet {
				return origin, port, false, true, fmt.Errorf("bili start didn't answer at %s: something else is answering there", origin)
			}
		case biliStarting:
			return origin, port, false, true, fmt.Errorf("bili start didn't answer at %s: still starting", origin)
		default:
			return origin, port, false, userSet, nil
		}
	} else if userSet {
		return origin, port, false, true, fmt.Errorf("bili start didn't answer at %s: something else is answering there", origin)
	}
	o, p, up, ferr := biliNextFree(origin, failed)
	if ferr != nil {
		return "", 0, false, false, ferr
	}
	return o, p, up, false, nil
}

// biliClassify is biliProbe, waiting out a 503 starting response until
// bili is ready, something else answers, or the wait ends.
func biliClassify(origin string) biliHeard {
	h := biliProbe(origin)
	if h != biliStarting {
		return h
	}
	return biliAwait(origin)
}

// biliAwait polls origin until it is up or plainly not bili.
func biliAwait(origin string) biliHeard {
	heard := biliStarting
	deadline := time.Now().Add(biliWait)
	for time.Now().Before(deadline) {
		heard = biliProbe(origin)
		if heard == biliUp || heard == biliOther {
			return heard
		}
		time.Sleep(250 * time.Millisecond)
	}
	return heard
}

// biliNextFree is a bili other than blocked. A registered loopback
// instance wins; otherwise the first lane port that is bili or has
// nothing on it. failed ports already lost a bind and are skipped.
// Occupied ports (another HTTP server, a non-HTTP TCP listener) are
// skipped too.
func biliNextFree(blocked string, failed map[int]bool) (origin string, port int, up bool, err error) {
	if o, p, ok := biliUpElsewhere(blocked); ok && !failed[p] {
		return o, p, true, nil
	}
	gw, _ := strconv.Atoi(gateway.Port())
	for p := biliZonePort; p < biliZonePort+biliPortSpan; p++ {
		if p == gw || failed[p] {
			continue
		}
		o := biliOriginOf(p)
		if o == blocked {
			continue
		}
		switch biliClassify(o) {
		case biliUp:
			return o, p, true, nil
		case biliDown:
			return o, p, false, nil
		}
	}
	return "", 0, false, fmt.Errorf("bili start didn't answer at %s: something else is answering there", blocked)
}

func biliUpElsewhere(blocked string) (origin string, port int, ok bool) {
	gw, _ := strconv.Atoi(gateway.Port())
	for _, o := range biliOrigins() {
		p, good := portOfOrigin(o)
		if !good || p == gw || biliOriginOf(p) == blocked {
			continue
		}
		if biliProbe(biliOriginOf(p)) == biliUp {
			return biliOriginOf(p), p, true
		}
	}
	return "", 0, false
}

func portOfOrigin(origin string) (int, bool) {
	i := strings.LastIndex(origin, ":")
	if i < 0 || i == len(origin)-1 {
		return 0, false
	}
	s := strings.TrimSuffix(origin[i+1:], "/")
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, false
	}
	return n, true
}

// biliServing says a bili magpie would use is already answering. A var
// for tests.
var biliServing = func() bool {
	origin, _, up, _, err := biliTarget()
	if err != nil || !up {
		return false
	}
	biliRemember(origin)
	return true
}

// biliServer is the bili start magpie runs: under ctx (KeepLinked's,
// which ends with magpie), stopped by cancel.
var biliServer struct {
	sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
}

func biliServeLog() string { return filepath.Join(settings.Dir(), "bili-start.log") }

// biliWait is how long a bili start just run has to answer.
var biliWait = 20 * time.Second

// biliServe has bili answer at the port biliListenPort names. One there
// already is used as it is, else magpie runs its own. A foreign server
// on the default 8787 is skipped for a port in bili's lane zone. A var
// for tests.
var biliServe = func(bin string) error {
	biliServer.Lock()
	defer biliServer.Unlock()
	failed := map[int]bool{}
	var last error
	for attempts := 0; attempts < biliPortSpan+1; attempts++ {
		origin, port, up, stick, err := biliTargetSkip(failed)
		if err != nil {
			return err
		}
		if up {
			biliRemember(origin)
			return nil
		}
		err, conflict := biliStartAt(bin, origin, port)
		if err == nil {
			biliRemember(origin)
			return nil
		}
		last = err
		// A user-chosen port stays put. A port that only looked free, then
		// refused the bind or turned out to be another program, does not.
		if !conflict || stick {
			return err
		}
		failed[port] = true
	}
	if last != nil {
		return last
	}
	return fmt.Errorf("bili start didn't answer: the fallback ports are taken")
}

// biliStartAt runs bili start --port and waits until that origin answers
// as bili. conflict means the port is taken: something else answered, or
// bili start reported EADDRINUSE. A 503 starting response is bili itself
// and is waited out, not a conflict.
func biliStartAt(bin, origin string, port int) (error, bool) {
	if biliServer.cancel != nil {
		biliServer.cancel()
		biliServer.cancel = nil
	}
	parent := biliServer.ctx
	if parent == nil {
		parent = context.Background()
	}
	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		return err, false
	}
	out, err := os.Create(biliServeLog())
	if err != nil {
		return err, false
	}
	ctx, cancel := context.WithCancel(parent)
	cmd := proc.CommandContext(ctx, bin, "start", "--port", strconv.Itoa(port))
	cmd.Stdin = nil
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Env = append(os.Environ(), "PATH="+filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := cmd.Start(); err != nil {
		cancel()
		out.Close()
		return fmt.Errorf("bili start: %w", err), false
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		out.Close()
		close(done)
	}()
	biliServer.cancel = cancel
	heard := biliDown
	for end := time.Now().Add(biliWait); time.Now().Before(end); {
		heard = biliProbe(origin)
		if heard == biliUp {
			return nil, false
		}
		if heard == biliOther {
			break
		}
		select {
		case <-done:
			end = time.Time{}
		case <-time.After(250 * time.Millisecond):
		}
	}
	cancel()
	biliServer.cancel = nil
	<-done
	said, _ := os.ReadFile(biliServeLog())
	text := string(said)
	why := lastLines(strings.TrimSpace(text), 3)
	conflict := heard == biliOther || biliBindConflict(text)
	if heard == biliOther {
		why = "something else is answering there"
	}
	if heard == biliStarting {
		why = "still starting"
	}
	if why == "" {
		why = "nothing answered"
	}
	return fmt.Errorf("bili start didn't answer at %s: %s", origin, why), conflict
}

// biliBindConflict is bili start failing to listen because the port was
// taken after the probe (Node's EADDRINUSE).
func biliBindConflict(text string) bool {
	if strings.Contains(text, "EADDRINUSE") {
		return true
	}
	return strings.Contains(strings.ToLower(text), "address already in use")
}

// biliStop ends the bili start magpie runs, if it runs one. A var for
// tests.
var biliStop = func() {
	biliServer.Lock()
	defer biliServer.Unlock()
	if biliServer.cancel != nil {
		biliServer.cancel()
		biliServer.cancel = nil
	}
}

// biliURLOn is whether any agent here goes through bili's prefix.
func biliURLOn(agents []*agent.Agent) bool {
	for _, a := range biliAgents(agents) {
		if l, ok := biliURLLanes[a.ID]; ok && l.on(a) {
			return true
		}
	}
	return false
}

// alignBiliURLs points every active URL-lane agent that already goes through
// a /bili/ prefix at the origin just resolved, so a Zed left on 8787 is not
// still sent there after the daemon moved (#7382). Only that outer prefix on
// magpie's own entries changes. Agents whose lane is off, a custom URL, path,
// or query, and a profile that is not on bili stay as they are. Caller holds
// biliMu.
func alignBiliURLs(agents []*agent.Agent) error {
	prefix := biliPrefixNow()
	var errs []error
	for _, a := range agents {
		l, ok := biliURLLanes[a.ID]
		if !ok || !l.on(a) {
			continue
		}
		errs = append(errs, agent.MoveBiliPrefix(a, prefix))
	}
	return errors.Join(errs...)
}

// setBiliURL puts an agent's magpie provider through bili's prefix, with
// bili start answering there first, or back on the gateway, and ends
// magpie's bili start when nothing goes through it any more.
func setBiliURL(a *agent.Agent, agents []*agent.Agent, bin string, on bool) (*BiliView, error) {
	b := newBackups()
	if err := b.keep(a.ID, a.Path); err != nil {
		return nil, err
	}
	l := biliURLLanes[a.ID]
	if on {
		if err := biliServe(bin); err != nil {
			return nil, err
		}
		prefix := biliPrefixNow()
		if err := l.set(a, prefix); err != nil {
			return nil, err
		}
		// Align other active URL-lane agents to the resolved prefix without
		// letting a broken file in an unrelated agent fail this switch after
		// a's own file and daemon have already been updated.
		for _, x := range agents {
			if x.ID == a.ID {
				continue
			}
			xl, ok := biliURLLanes[x.ID]
			if !ok || !xl.on(x) {
				continue
			}
			if err := b.keep(x.ID, x.Path); err != nil {
				log.Println("library: billion-context:", err)
				continue
			}
			if err := agent.MoveBiliPrefix(x, prefix); err != nil {
				log.Println("library: billion-context:", err)
			}
		}
	} else {
		if err := l.set(a, ""); err != nil {
			return nil, err
		}
		if !biliURLOn(agents) {
			biliStop()
		}
	}
	v := readBili(agents)
	v.Restart, v.Backup = []string{a.ID}, b.dir
	if b.dir != "" {
		pruneBackups()
	}
	return v, nil
}

// keepBiliServe, from KeepLinked, keeps bili start answering while an
// agent goes through its prefix: at magpie's start, and after it stopped.
// ctx is KeepLinked's, which ends magpie's bili start with magpie.
func keepBiliServe(ctx context.Context) {
	biliServer.Lock()
	if biliServer.ctx == nil {
		biliServer.ctx = ctx
	}
	biliServer.Unlock()
	biliMu.Lock()
	agents := detected()
	on := biliURLOn(agents)
	biliMu.Unlock()
	if !on {
		return
	}
	bin := biliPath()
	if bin == "" {
		return
	}
	if !biliServing() {
		if err := biliServe(bin); err != nil {
			log.Println("library: billion-context:", err)
			return
		}
	}
	// SetBili holds biliMu across its own write. Re-read under that lock
	// so a disable that landed while bili was starting is not undone by
	// writing the prefix back onto the direct URL.
	biliMu.Lock()
	defer biliMu.Unlock()
	agents = detected()
	if !biliURLOn(agents) {
		biliStop()
		return
	}
	if err := alignBiliURLs(agents); err != nil {
		log.Println("library: billion-context:", err)
	}
}
