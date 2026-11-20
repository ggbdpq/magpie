package library

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/testenv"
)

// TestBiliUsesConfiguredPortWhen8787Answers401 is #7382 (wxy0626):
// something else owns 127.0.0.1:8787 and answers 401, and
// billion-context.json says "port": 18787, which bili start itself
// listens on. Enabling the shared daemon must probe, start and write
// the /bili/ prefix on that port, not on 8787.
func TestBiliUsesConfiguredPortWhen8787Answers401(t *testing.T) {
	h := sandbox(t)
	for _, k := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(k, "")
	}
	unsetEnv(t, "ACP_PORT", "PORT")
	listenHTTP(t, "127.0.0.1:8787", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "missing or bad bearer token")
	})

	wasWait := biliWait
	biliWait = 500 * time.Millisecond
	t.Cleanup(func() {
		biliWait = wasWait
		resetBiliListen()
	})

	dir := filepath.Join(h, "zed-config")
	t.Setenv("MAGPIE_ZED_CONFIG_DIR", dir)
	zs := filepath.Join(dir, "settings.json")
	gw := gateway.URL() + "/v1"
	write(t, zs, `{"language_models":{"openai_compatible":{"magpie":{"api_url":"`+gw+`","available_models":[]}}}}`)

	bin, log := biliRecorder(t, h)
	wasPath := biliPath
	biliPath = func() string { return bin }
	var through []string
	wasThrough := zedThrough
	zedThrough = func(a *agent.Agent, prefix string) error {
		through = append(through, prefix)
		body := read(t, a.Path)
		out := strings.Replace(body, gw, prefix+gw, 1)
		return os.WriteFile(a.Path, []byte(out), 0o600)
	}
	t.Cleanup(func() {
		biliPath = wasPath
		zedThrough = wasThrough
	})

	healthy := func(t *testing.T) {
		t.Helper()
		listenHTTP(t, "127.0.0.1:18787", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/__bili/health" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true}`)
		})
	}
	clearRuns := func(t *testing.T) {
		t.Helper()
		os.Remove(log)
		through = nil
		resetBiliListen()
	}
	wantPrefix := "http://127.0.0.1:18787/bili/"

	t.Run("configured port already answering", func(t *testing.T) {
		clearRuns(t)
		write(t, BiliConfigFile(), "// kept\n{\n  \"port\": 18787\n}\n")
		healthy(t)
		if _, err := SetBili("zed", true); err != nil {
			t.Fatal(err)
		}
		if strings.Join(through, " ") != wantPrefix {
			t.Fatalf("prefix %q, want %s", through, wantPrefix)
		}
		if got := read(t, zs); !strings.Contains(got, `"api_url":"`+wantPrefix+gw+`"`) || strings.Contains(got, "127.0.0.1:8787") {
			t.Fatalf("zed api_url:\n%s", got)
		}
		for _, line := range biliRuns(t, log) {
			if strings.Contains(line, "start") {
				t.Fatalf("bili start ran while 18787 was already bili: %q", biliRuns(t, log))
			}
		}
	})

	t.Run("bili start is given the configured port", func(t *testing.T) {
		clearRuns(t)
		write(t, BiliConfigFile(), `{"port":18787}`+"\n")
		_, err := SetBili("zed", true)
		if err == nil {
			t.Fatal("expected bili start to fail when 18787 never answers")
		}
		if !strings.Contains(err.Error(), "127.0.0.1:18787") || strings.Contains(err.Error(), "127.0.0.1:8787") {
			t.Fatalf("error %q, want the configured port and not 8787", err)
		}
		if !slices.Contains(biliRuns(t, log), "start --port 18787") {
			t.Fatalf("runs %q, want start --port 18787", biliRuns(t, log))
		}
	})

	t.Run("no configured port leaves a foreign 8787", func(t *testing.T) {
		clearRuns(t)
		os.Remove(BiliConfigFile())
		healthy(t)
		if _, err := SetBili("zed", true); err != nil {
			t.Fatal(err)
		}
		if strings.Join(through, " ") != wantPrefix {
			t.Fatalf("prefix %q, want %s (the lane already answering)", through, wantPrefix)
		}
		for _, line := range biliRuns(t, log) {
			if strings.Contains(line, "8787") {
				t.Fatalf("started on 8787: %q", biliRuns(t, log))
			}
		}
	})

	t.Run("a configured port that is itself foreign is kept", func(t *testing.T) {
		clearRuns(t)
		write(t, BiliConfigFile(), `{"port":18787}`+"\n")
		listenHTTP(t, "127.0.0.1:18787", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, "missing or bad bearer token")
		})
		_, err := SetBili("zed", true)
		if err == nil || !strings.Contains(err.Error(), "127.0.0.1:18787") || strings.Contains(err.Error(), "127.0.0.1:8787") {
			t.Fatalf("error %v, want the configured port refused and 8787 left alone", err)
		}
		for _, line := range biliRuns(t, log) {
			if strings.Contains(line, "start") {
				t.Fatalf("bili start ran on a port something else owns: %q", biliRuns(t, log))
			}
		}
	})

	t.Run("a port out of range is not replaced with 8787", func(t *testing.T) {
		clearRuns(t)
		write(t, BiliConfigFile(), `{"port":70000}`+"\n")
		healthy(t)
		_, err := SetBili("zed", true)
		if err == nil || strings.Contains(err.Error(), "127.0.0.1:8787") || !strings.Contains(err.Error(), "70000") {
			t.Fatalf("error %v, want the bad port named and 8787 unused", err)
		}
		for _, line := range biliRuns(t, log) {
			if strings.Contains(line, "start") {
				t.Fatalf("bili start ran despite a bad port: %q", biliRuns(t, log))
			}
		}
	})

	t.Run("PORT wins when ACP_PORT is unset, the way bili start does", func(t *testing.T) {
		clearRuns(t)
		os.Remove(BiliConfigFile())
		t.Setenv("PORT", "18787")
		healthy(t)
		if _, err := SetBili("zed", true); err != nil {
			t.Fatal(err)
		}
		if strings.Join(through, " ") != wantPrefix {
			t.Fatalf("prefix %q, want %s from PORT", through, wantPrefix)
		}
	})

	t.Run("ACP_PORT wins over the file, the way bili start does", func(t *testing.T) {
		clearRuns(t)
		write(t, BiliConfigFile(), `{"port":8787}`+"\n")
		t.Setenv("ACP_PORT", "18787")
		healthy(t)
		if _, err := SetBili("zed", true); err != nil {
			t.Fatal(err)
		}
		if strings.Join(through, " ") != wantPrefix {
			t.Fatalf("prefix %q, want %s from ACP_PORT", through, wantPrefix)
		}
	})

	t.Run("an unreadable or malformed config falls back to default 8787 like bili", func(t *testing.T) {
		clearRuns(t)
		p := BiliConfigFile()
		write(t, p, "{ not valid json\n")
		healthy(t)
		if _, err := SetBili("zed", true); err != nil {
			t.Fatalf("malformed config blocked startup instead of falling back to 8787: %v", err)
		}
		if strings.Join(through, " ") != wantPrefix {
			t.Fatalf("prefix %q, want %s after fallback off foreign 8787", through, wantPrefix)
		}
		if runtime.GOOS == "windows" {
			return
		}
		clearRuns(t)
		write(t, p, `{"port":19999}`+"\n")
		if err := os.Chmod(p, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(p, 0o600) })
		if _, err := SetBili("zed", true); err != nil {
			t.Fatalf("unreadable config blocked startup instead of falling back to 8787: %v", err)
		}
		if strings.Join(through, " ") != wantPrefix {
			t.Fatalf("prefix %q, want %s after unreadable config fallback", through, wantPrefix)
		}
	})
}

// TestBiliDefaultPortStays8787WhenItIsFree: with no port in the file and
// nothing on 8787, the shared daemon is still bili's own default.
func TestBiliDefaultPortStays8787WhenItIsFree(t *testing.T) {
	h := sandbox(t)
	for _, k := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(k, "")
	}
	unsetEnv(t, "ACP_PORT", "PORT")
	ln, err := net.Listen("tcp", "127.0.0.1:8787")
	if err != nil {
		t.Fatalf("8787 is not free, so this can't tell the default from a fallback: %v", err)
	}
	ln.Close()
	wasWait := biliWait
	biliWait = 500 * time.Millisecond
	t.Cleanup(func() {
		biliWait = wasWait
		resetBiliListen()
	})
	bin, log := biliRecorder(t, h)
	wasPath := biliPath
	biliPath = func() string { return bin }
	t.Cleanup(func() { biliPath = wasPath })
	os.Remove(BiliConfigFile())
	err = biliServe(bin)
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1:8787") {
		t.Fatalf("error %v, want the default port", err)
	}
	if !slices.Contains(biliRuns(t, log), "start --port 8787") {
		t.Fatalf("runs %q, want start --port 8787", biliRuns(t, log))
	}
}

func listenHTTP(t *testing.T, addr string, h http.HandlerFunc) {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listen %s: %v", addr, err)
	}
	srv := &http.Server{Handler: h}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

func biliRecorder(t *testing.T, h string) (bin, log string) {
	t.Helper()
	log = filepath.Join(h, "bili-runs.txt")
	if runtime.GOOS == "windows" {
		bin = filepath.Join(h, "bin", "bili.cmd")
		write(t, bin, "@echo off\r\necho %*>>"+log+"\r\nexit /b 0\r\n")
		return bin, log
	}
	bin = filepath.Join(h, "bin", "bili")
	testenv.Program(t, bin, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+shSingle(log)+"\nexit 0\n")
	return bin, log
}

func shSingle(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// unsetEnv clears a variable for the test. An empty string is not unset:
// bili treats ACP_PORT="" and PORT="" as set, and parseInt of "" is an error.
func unsetEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
}

// TestBiliStartWaitsOutStarting503 is billion-context binding its port
// before registration finishes: 503 {"ok":false,"reason":"starting"} is
// bili, and the child just started is kept until health is ok.
func TestBiliStartWaitsOutStarting503(t *testing.T) {
	h := sandbox(t)
	unsetEnv(t, "ACP_PORT", "PORT")
	var n atomic.Int32
	listenHTTP(t, "127.0.0.1:8787", func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"ok":false,"reason":"starting"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	was := biliWait
	biliWait = 3 * time.Second
	t.Cleanup(func() {
		biliWait = was
		resetBiliListen()
	})
	bin := filepath.Join(h, "bin", "bili")
	if runtime.GOOS == "windows" {
		bin = filepath.Join(h, "bin", "bili.cmd")
		write(t, bin, "@echo off\r\n:loop\r\ngoto loop\r\n")
	} else {
		// /bin/sleep, not sleep: the test's PATH is only its own bin.
		testenv.Program(t, bin, "#!/bin/sh\n/bin/sleep 30\n")
	}
	err, conflict := biliStartAt(bin, "http://127.0.0.1:8787", 8787)
	if err != nil || conflict {
		t.Fatalf("starting 503 was treated as another program: conflict %v, err %v", conflict, err)
	}
}

// TestBiliStartWaitsOutSlowFirstProbe: our own just-started child may bind
// its port and take longer than biliProbeTimeout to answer the first health
// probe. The timeout is biliStalled, not biliOther, so the child stays alive
// until the next probe succeeds.
func TestBiliStartWaitsOutSlowFirstProbe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell sleep helper is POSIX-only")
	}
	h := sandbox(t)
	unsetEnv(t, "ACP_PORT", "PORT")
	var n atomic.Int32
	listenHTTP(t, "127.0.0.1:28787", func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			time.Sleep(350 * time.Millisecond)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	wasT, wasW := biliProbeTimeout, biliWait
	biliProbeTimeout, biliWait = 150*time.Millisecond, 3*time.Second
	t.Cleanup(func() {
		biliProbeTimeout, biliWait = wasT, wasW
		resetBiliListen()
	})
	bin := filepath.Join(h, "bin", "bili")
	testenv.Program(t, bin, "#!/bin/sh\n/bin/sleep 30\n")
	err, conflict := biliStartAt(bin, "http://127.0.0.1:28787", 28787)
	if err != nil || conflict {
		t.Fatalf("slow first health probe killed own child: conflict %v, err %v", conflict, err)
	}
}

// TestBiliProbeDistinguishesDialFailureFromStalledListener: a TCP connect
// failure (such as a dial timeout during Windows' ~2s loopback RST retry)
// reports biliDown so free ports are not treated as taken, while a listener
// that accepts TCP and stalls or drops without HTTP reports biliStalled.
func TestBiliProbeDistinguishesDialFailureFromStalledListener(t *testing.T) {
	t.Run("dial timeout is biliDown", func(t *testing.T) {
		wasDial := biliDial
		biliDial = func(context.Context, string, string) (net.Conn, error) {
			return nil, context.DeadlineExceeded
		}
		t.Cleanup(func() { biliDial = wasDial })
		if got := biliProbe("http://127.0.0.1:28788"); got != biliDown {
			t.Fatalf("dial timeout gave %v, want biliDown (%v)", got, biliDown)
		}
	})

	t.Run("connected silent listener is biliStalled", func(t *testing.T) {
		wasT := biliProbeTimeout
		biliProbeTimeout = 120 * time.Millisecond
		t.Cleanup(func() { biliProbeTimeout = wasT })
		hold := make(chan struct{})
		t.Cleanup(func() { close(hold) })
		listenRaw(t, "127.0.0.1:28789", func(c net.Conn) {
			go func() {
				defer c.Close()
				<-hold
			}()
		})
		if got := biliProbe("http://127.0.0.1:28789"); got != biliStalled {
			t.Fatalf("stalled TCP listener gave %v, want biliStalled (%v)", got, biliStalled)
		}
	})
}

// TestBiliPortParseMatchesBili is src/config.ts loadOptions: parseInt of
// ACP_PORT ?? PORT ?? (file port ?? 8787). null falls back, a fraction
// truncates, and a set empty variable does not fall through.
func TestBiliPortParseMatchesBili(t *testing.T) {
	sandbox(t)
	unsetEnv(t, "ACP_PORT", "PORT", "BILI_CONFIG_FILE")

	t.Run("null port is the default", func(t *testing.T) {
		write(t, BiliConfigFile(), `{"port":null}`+"\n")
		p, user, err := biliListenPort()
		if err != nil || p != biliDefaultPort || user {
			t.Fatalf("port %d user %v err %v, want %d and not a user port", p, user, err, biliDefaultPort)
		}
	})

	t.Run("8787.5 is 8787", func(t *testing.T) {
		write(t, BiliConfigFile(), `{"port":8787.5}`+"\n")
		p, user, err := biliListenPort()
		if err != nil || p != 8787 || !user {
			t.Fatalf("port %d user %v err %v, want 8787 set by the file", p, user, err)
		}
	})

	t.Run("empty ACP_PORT does not fall through", func(t *testing.T) {
		t.Setenv("ACP_PORT", "")
		t.Setenv("PORT", "18787")
		write(t, BiliConfigFile(), `{"port":18787}`+"\n")
		if _, _, err := biliListenPort(); err == nil {
			t.Fatal("ACP_PORT=\"\" fell through to PORT or the file")
		}
	})

	t.Run("empty PORT does not fall through", func(t *testing.T) {
		t.Setenv("PORT", "")
		write(t, BiliConfigFile(), `{"port":18787}`+"\n")
		if _, _, err := biliListenPort(); err == nil {
			t.Fatal("PORT=\"\" fell through to the file")
		}
	})

	t.Run("a missing named config is not the default port", func(t *testing.T) {
		t.Setenv("BILI_CONFIG_FILE", "no-such-model")
		if _, _, err := biliListenPort(); err == nil || !strings.Contains(err.Error(), "no-such-model") {
			t.Fatalf("err %v, want the missing name and not port 8787", err)
		}
	})

	t.Run("whitespace-only BILI_CONFIG_FILE falls back to default", func(t *testing.T) {
		os.Remove(BiliConfigFile())
		t.Setenv("BILI_CONFIG_FILE", "   ")
		p, user, err := biliListenPort()
		if err != nil || p != biliDefaultPort || user {
			t.Fatalf("port %d user %v err %v, want default %d", p, user, err, biliDefaultPort)
		}
	})

	t.Run("malformed config falls back to default", func(t *testing.T) {
		unsetEnv(t, "BILI_CONFIG_FILE")
		write(t, BiliConfigFile(), "{ not json\n")
		p, user, err := biliListenPort()
		if err != nil || p != biliDefaultPort || user {
			t.Fatalf("port %d user %v err %v, want default %d", p, user, err, biliDefaultPort)
		}
	})
}

// TestBiliNamedConfigPortIsNotOverridden: BILI_CONFIG_FILE=work is
// billion-context/work.json, and bili start is given that file's port.
// The default file's port is not passed over it.
func TestBiliNamedConfigPortIsNotOverridden(t *testing.T) {
	h := sandbox(t)
	unsetEnv(t, "ACP_PORT", "PORT")
	for _, k := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(k, "")
	}
	write(t, filepath.Join(h, ".config", "billion-context", "billion-context.json"), `{"port":8787}`+"\n")
	write(t, filepath.Join(h, ".config", "billion-context", "work.json"), `{"port":19001}`+"\n")
	t.Setenv("BILI_CONFIG_FILE", "work")
	was := biliWait
	biliWait = 400 * time.Millisecond
	t.Cleanup(func() {
		biliWait = was
		resetBiliListen()
	})
	bin, log := biliRecorder(t, h)
	wasPath := biliPath
	biliPath = func() string { return bin }
	t.Cleanup(func() { biliPath = wasPath })
	err := biliServe(bin)
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1:19001") || strings.Contains(err.Error(), "127.0.0.1:8787") {
		t.Fatalf("error %v, want the named config's port and not 8787", err)
	}
	if !slices.Contains(biliRuns(t, log), "start --port 19001") {
		t.Fatalf("runs %q, want start --port 19001", biliRuns(t, log))
	}
	for _, line := range biliRuns(t, log) {
		if strings.Contains(line, "8787") {
			t.Fatalf("default port overridden the named config: %q", biliRuns(t, log))
		}
	}
}

// TestBiliFallbackSkipsOccupiedLanePorts: a foreign 8787, a non-HTTP
// listener on 18787, and a bind that fails on a port the probe thought
// was free, each move on to 18788.
func TestBiliFallbackSkipsOccupiedLanePorts(t *testing.T) {
	h := sandbox(t)
	unsetEnv(t, "ACP_PORT", "PORT", "BILI_CONFIG_FILE")
	for _, k := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(k, "")
	}
	listenHTTP(t, "127.0.0.1:8787", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "missing or bad bearer token")
	})
	was := biliWait
	biliWait = 400 * time.Millisecond
	t.Cleanup(func() {
		biliWait = was
		resetBiliListen()
	})
	os.Remove(BiliConfigFile())

	t.Run("non-HTTP TCP on 18787", func(t *testing.T) {
		resetBiliListen()
		listenRaw(t, "127.0.0.1:18787", nil)
		bin, log := biliRecorder(t, h)
		err := biliServe(bin)
		if err == nil || !strings.Contains(err.Error(), "127.0.0.1:18788") {
			t.Fatalf("error %v, want the next free port 18788", err)
		}
		runs := biliRuns(t, log)
		if slices.Contains(runs, "start --port 18787") || !slices.Contains(runs, "start --port 18788") {
			t.Fatalf("runs %q, want 18788 and not 18787", runs)
		}
	})

	t.Run("bind conflict on 18787", func(t *testing.T) {
		resetBiliListen()
		log := filepath.Join(h, "bind-runs.txt")
		os.Remove(log)
		bin := biliBindScript(t, h, log)
		err := biliServe(bin)
		if err == nil || !strings.Contains(err.Error(), "127.0.0.1:18788") {
			t.Fatalf("error %v, want the next free port 18788", err)
		}
		runs := biliRuns(t, log)
		if !slices.Contains(runs, "start --port 18787") || !slices.Contains(runs, "start --port 18788") {
			t.Fatalf("runs %q, want 18787 tried and 18788 next", runs)
		}
	})
}

func listenRaw(t *testing.T, addr string, on func(net.Conn)) {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listen %s: %v", addr, err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if on != nil {
				on(c)
				continue
			}
			c.Close()
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
}

func biliBindScript(t *testing.T, h, log string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		bin := filepath.Join(h, "bin", "bili-bind.cmd")
		write(t, bin, "@echo off\r\necho %*>>"+log+"\r\necho %*| find \"18787\" >nul && (echo Error: listen EADDRINUSE: address already in use 127.0.0.1:18787& exit /b 1)\r\nexit /b 0\r\n")
		return bin
	}
	bin := filepath.Join(h, "bin", "bili-bind")
	testenv.Program(t, bin, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+shSingle(log)+"\ncase \"$*\" in\n*18787*)\nprintf '%s\\n' 'Error: listen EADDRINUSE: address already in use 127.0.0.1:18787'\nexit 1\n;;\nesac\nexit 0\n")
	return bin
}

func biliRuns(t *testing.T, log string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	text := strings.TrimSpace(string(b))
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}
