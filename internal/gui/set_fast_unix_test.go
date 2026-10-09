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
// on the next click — the pick took 5–10s to show. Here the devin CLI takes
// 20s to answer and a provider's model endpoint never answers; the set, and
// the state after it, must not wait on either, and Claude Code's config is
// written as before. The guarantee is told twice: by the fake's landing
// marker, which no request that didn't wait can see, and by a bound with
// real slack — a wall-clock one this tight failed at 1.6s under a full
// go test run (#1375).
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

	// a devin CLI that takes 20s over every answer and marks the answer as
	// landed: the set and the state below must not wait on it, which the
	// marker tells without a clock. Anything that waited would come back
	// after the fake's 20s with the marker written; nothing that doesn't
	// wait can see it, the whole test body running long before then.
	devin := filepath.Join(bin, "devin")
	answered := filepath.Join(h, "devin-answered")
	testenv.Program(t, devin, "#!/bin/sh\nsleep 20\ntouch "+answered+"\n")
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
		// slack on purpose: a set that waits on the devin CLI takes the
		// fake's 20s and one that waits on the provider never comes back,
		// while a fast one is milliseconds — the bound catches the waiting
		// early; the marker below is what pins the guarantee
		if took > 10*time.Second {
			t.Errorf("set %s took %v: it waited on the devin CLI or the provider", v, took)
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
		if took := time.Since(start); took > 10*time.Second {
			t.Errorf("the state after set %s took %v", v, took)
		}
	}

	// no devin answer landed while the requests ran. Whatever the machine's
	// load, a request that came back inside the bound cannot have waited on
	// the CLI, whose answers land only after the fake's 20s; a marker here
	// says some request did wait, however long it took.
	if _, err := os.Stat(answered); err == nil {
		t.Error("a devin answer landed mid-test: a set or the state waited on the CLI")
	}

	// what is written is what it was: the last pick, Anthropic's own
	// model, off magpie's gateway
	if m, _ := edit.GetJSON(settingsPath, "model"); m != "claude-sonnet-5-5" {
		t.Errorf("settings.json model %q", m)
	}
	if u, _ := edit.GetJSON(settingsPath, "env.ANTHROPIC_BASE_URL"); u != "" {
		t.Errorf("settings.json still routed to %q", u)
	}
}
