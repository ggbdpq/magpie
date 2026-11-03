package cli

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
)

// magpie update tells the magpie running what it put in, and says what
// came of it: a restart, a wait, magpie serve, an older magpie (404), or
// none running.
func TestUpdateAsksTheRunningMagpieToRestart(t *testing.T) {
	var reply gateway.RestartReply
	var asked string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != gateway.RestartPath {
			http.NotFound(w, r)
			return
		}
		b, _ := io.ReadAll(r.Body)
		asked = string(b)
		json.NewEncoder(w).Encode(reply)
	}))
	defer ts.Close()
	for _, c := range []struct {
		reply gateway.RestartReply
		want  string
	}{
		{gateway.RestartReply{State: "restarting", Version: "v0.1.1", Handover: true}, "restarts into v0.1.2 now, handing its gateway over"},
		{gateway.RestartReply{State: "waiting", Version: "v0.1.1", Busy: &gateway.Busy{Requests: 2}}, "once the 2 request(s)"},
		{gateway.RestartReply{State: "unsupported", Version: "v0.1.1"}, "magpie serve"},
		{gateway.RestartReply{State: "current", Version: "v0.1.1"}, "another copy"},
	} {
		reply = c.reply
		if got := restartRunning(ts.URL, "v0.1.2"); !strings.Contains(got, c.want) {
			t.Errorf("%s: %q", c.reply.State, got)
		}
		if asked != `{"version":"v0.1.2"}` {
			t.Errorf("asked %q", asked)
		}
	}
	old := httptest.NewServer(http.NotFoundHandler())
	defer old.Close()
	if got := restartRunning(old.URL, "v0.1.2"); !strings.Contains(got, "quit and reopen") {
		t.Errorf("an older magpie: %q", got)
	}
	old.Close()
	if got := restartRunning(old.URL, "v0.1.2"); !strings.Contains(got, "isn't running") {
		t.Errorf("none running: %q", got)
	}
}

// magpie update restarts magpie's own service when it was running the
// binary the update replaced (on the Mac one whose file the swap has
// deleted), and says so; the ask to the magpie serving the gateway is
// skipped then, and made as before when there was nothing to restart.
func TestUpdateRestartsTheService(t *testing.T) {
	was := serviceRestart
	defer func() { serviceRestart = was }()
	// running: restarted, said
	serviceRestart = func() (bool, error) { return true, nil }
	if got := restartService("0.2.0"); got != "the magpie service restarts into 0.2.0 now" {
		t.Errorf("a running service: %q", got)
	}
	// not running, not installed: nothing said, the ask follows
	serviceRestart = func() (bool, error) { return false, nil }
	if got := restartService("0.2.0"); got != "" {
		t.Errorf("none running: %q", got)
	}
	// a restart that failed is said, and the ask follows it
	serviceRestart = func() (bool, error) { return true, errors.New("no such process") }
	if got := restartService("0.2.0"); got != "the magpie service couldn't be restarted: no such process" {
		t.Errorf("a failed restart: %q", got)
	}
}
