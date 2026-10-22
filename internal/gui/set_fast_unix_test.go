//go:build !windows

package gui

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// Picking Claude Code's model answers at once, although /api/set answers
// with the whole state: with Devin installed, every state asked `devin
// models list` again (several times: its model's family, its options, its
// efforts), each taking seconds, and a CLI that gave no list was asked anew
// on the next click — the pick took 5–10s to show. Here the devin CLI hangs
// and a provider's model endpoint never answers; the set, and the state
// after it, must not wait on either, and Claude Code's config is written as
// before. That they didn't wait is told by order, not a clock: the fake
// blocks until the test releases it, after the requests, so its answer can
// only land once nothing was waiting on it — whatever the load. The load
// itself is why the latency check reads the machine first: a fixed clock
// failed at 1.6s, and its 10s successor at 13–31s, both on loaded runs
// (#1375); the guard is a bare round trip multiplied, and a set past it
// stops the test, since anything read after a wait that deep would be the
// machine's, not the code's.
func TestSetAnswersWhileDevinAndProvidersHang(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(h, ".local", "share"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	bin := filepath.Join(h, "bin")
	os.MkdirAll(bin, 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")

	// a devin CLI that hangs until the test releases it and only then
	// marks its answer landed. The release is written after the requests,
	// so no answer can land while they run, on any load — a wait on the
	// CLI and a slow box can't be told apart by a clock, and this needs
	// none. It sleeps in short steps and ends as soon as the test does;
	// the minute cap is for a test that never releases, and the asks it
	// is given time it out at their own 30s, killing a run mid-wait — a
	// normal end here, not a runaway (the running/ census the clocked
	// fake served counted those kills as runaways, so it went with it).
	devin := filepath.Join(bin, "devin")
	answered := filepath.Join(h, "devin-answered")
	release := filepath.Join(h, "devin-release")
	stop := filepath.Join(h, "devin-stop")
	testenv.Program(t, devin, "#!/bin/sh\n"+
		"i=0\n"+
		"while [ $i -lt 600 ] && [ ! -e '"+release+"' ] && [ ! -e '"+stop+"' ]; do sleep 0.1; i=$((i+1)); done\n"+
		"if [ -e '"+release+"' ] && [ ! -e '"+stop+"' ]; then touch '"+answered+"'; fi\n")
	t.Cleanup(func() { os.WriteFile(stop, nil, 0o644) })
	old := provider.DevinExecutable
	provider.DevinExecutable = func() string { return devin }
	t.Cleanup(func() { provider.DevinExecutable = old })
	os.MkdirAll(filepath.Join(h, ".config", "devin"), 0o755)
	os.WriteFile(filepath.Join(h, ".config", "devin", "config.json"), []byte(`{"agent":{"model":"claude-opus-5-5-high"}}`), 0o644)

	// a provider whose endpoint takes the connection and never answers
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		var held []net.Conn
		for {
			c, err := ln.Accept()
			if err != nil {
				for _, c := range held {
					c.Close()
				}
				return
			}
			held = append(held, c)
		}
	}()
	if _, err := provider.Add(provider.Provider{ID: "slow", Name: "Slow", Chat: "http://" + ln.Addr().String() + "/v1", Key: "sk-slow", Models: []string{"slow-1"}}); err != nil {
		t.Fatal(err)
	}

	settingsPath := filepath.Join(h, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(settingsPath), 0o755)
	os.WriteFile(settingsPath, []byte(`{"model":"sonnet"}`), 0o644)

	srv := httptest.NewServer(Handler(nil, nil))
	t.Cleanup(srv.Close)

	// what a request costs when nothing waits: one bare round trip of this
	// server, measured here, is the unit the latency guard thinks in. A set
	// does far more work than the trip (the whole state drawn, the config
	// written), so the guard is the trip times 200, at least 15s — so an
	// unloaded run still catches a wait — and at most 60s, so a hung set
	// still ends the test within the minute. A fixed clock was 1.5s and
	// failed at 1.6s under a full run, and its 10s successor failed at
	// 13–31s (#1375); whatever slows the sets slows this trip the same way,
	// so the guard moves with the machine instead of against it.
	ctrl := time.Now()
	cres, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	cres.Body.Close()
	trip := time.Since(ctrl)
	guard := 200 * trip
	if guard < 15*time.Second {
		guard = 15 * time.Second
	}
	if guard > 60*time.Second {
		guard = 60 * time.Second
	}
	set := func(value string) (time.Duration, stateJSON) {
		t.Helper()
		start := time.Now()
		res, err := http.Post(srv.URL+"/api/set", "application/json", strings.NewReader(`{"agent":"claude","field":"model","value":"`+value+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		took := time.Since(start)
		var s stateJSON
		if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&s) != nil {
			t.Fatalf("set %s: %d", value, res.StatusCode)
		}
		return took, s
	}
	shown := func(s stateJSON) string {
		for _, a := range s.Agents {
			if a.ID == "claude" {
				for _, f := range a.Fields {
					if f.Key == "model" {
						return f.Value
					}
				}
			}
		}
		return "?"
	}

	for _, v := range []string{"claude-opus-5-5", "slow/slow-1", "claude-sonnet-5-5"} {
		took, s := set(v)
		t.Logf("set %s: %v", v, took)
		// a set past the guard stops the test here: the checks below only
		// vouch for a run that stayed this fast, and one clock failure
		// must not pile onto another (the old 10s bound kept counting
		// through its own failures until the run outlasted the fake and
		// failed on the load it had measured, not on a wait)
		if took > guard {
			t.Fatalf("set %s took %v (guard %v, a bare round trip %v): it waited on the devin CLI or the provider", v, took, guard, trip)
		}
		if got := shown(s); got != v {
			t.Errorf("set %s: the state shows %q", v, got)
		}
		start := time.Now()
		res, err := http.Get(srv.URL + "/api/state")
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if took := time.Since(start); took > guard {
			t.Fatalf("the state after set %s took %v (guard %v, a bare round trip %v)", v, took, guard, trip)
		}
	}

	// no answer may have landed: the fake marks only once released, and
	// the release is written below, after this check — a marker here says
	// the gating leaked, which no speed of the machine explains
	if _, err := os.Stat(answered); err == nil {
		t.Fatal("a devin answer landed before the requests were done: a set or the state waited on the devin CLI")
	}
	os.WriteFile(release, nil, 0o644)

	// what is written is what it was: the last pick, Anthropic's own
	// model, off magpie's gateway
	if m, _ := edit.GetJSON(settingsPath, "model"); m != "claude-sonnet-5-5" {
		t.Errorf("settings.json model %q", m)
	}
	if u, _ := edit.GetJSON(settingsPath, "env.ANTHROPIC_BASE_URL"); u != "" {
		t.Errorf("settings.json still routed to %q", u)
	}
}
