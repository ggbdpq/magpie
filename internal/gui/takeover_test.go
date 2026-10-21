package gui

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
)

// A magpie that found the gateway taken serves it once the one that had it
// is gone (one left running from before an update, a magpie serve).
func TestGatewayTakenOver(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAGPIE_ADDR", ln.Addr().String())
	other := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"name":"magpie"}`))
	})}
	go other.Serve(ln)
	defer other.Close()
	// the watch and the gateway it takes up end with the test, before its
	// HOME and MAGPIE_ADDR are put back
	ctx, stop := context.WithCancel(context.Background())
	watched := make(chan struct{})
	t.Cleanup(func() {
		stop()
		<-watched
		gatewayMu.Lock()
		defer gatewayMu.Unlock()
		if served.Load() != nil && servedRun.stop != nil {
			servedRun.stop()
			<-servedRun.done
		}
		served.Store(nil)
	})
	go func() {
		defer close(watched)
		watchGateway(ctx, 50*time.Millisecond)
	}()
	if serveGateway() != nil {
		t.Fatal("served over another magpie")
	}
	time.Sleep(200 * time.Millisecond)
	if served.Load() != nil {
		t.Fatal("took the gateway while the other had it")
	}
	other.Close()
	for deadline := time.Now().Add(5 * time.Second); served.Load() == nil || !gateway.Running(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the gateway wasn't taken up")
		}
	}
}

// A magpie that holds the gateway's port but doesn't answer in time (the
// service busy on a small server, or starting) still has it: magpie web
// beside it doesn't listen a second time, which only failed with "bind:
// address already in use" (K.Liu on Discord), and leaves the port to it.
func TestGatewayNotServedBesideASlowOne(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAGPIE_ADDR", ln.Addr().String())
	slow := make(chan struct{})
	other := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-slow
		w.Write([]byte(`{"name":"magpie"}`))
	})}
	go other.Serve(ln)
	defer other.Close()
	defer close(slow)
	gw := serveGateway()
	t.Cleanup(func() {
		gatewayMu.Lock()
		defer gatewayMu.Unlock()
		if served.Load() != nil && servedRun.stop != nil {
			servedRun.stop()
			<-servedRun.done
		}
		served.Store(nil)
		takenSaid.Store(false)
	})
	if gw != nil {
		t.Fatal("listened for the gateway on a port another magpie holds")
	}
}

// Told onto the gateway's own address, the page doesn't take it: one port
// holds one listener, and the gateway keeps its — the agents point there,
// while a page on it was only ever taken for another magpie's, which this
// one waited for for as long as it lived, and /v1 answered the sign-in
// page instead (#3209). The page goes beside it, and the gateway serves.
func TestWebOnTheGatewaysOwnAddress(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("MAGPIE_WEB_KEY", "")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gw := ln.Addr().String()
	ln.Close()
	t.Setenv("MAGPIE_ADDR", gw)
	// the updater starts with the page: its first look at the feed waits
	// five seconds, past this test, and asks then only once it is due
	updates.mu.Lock()
	updates.asked = time.Now()
	updates.mu.Unlock()

	w, err := StartWeb(gw, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		w.srv.Close()
		webPage.Store(false)
		gatewayMu.Lock()
		defer gatewayMu.Unlock()
		if served.Load() != nil && servedRun.stop != nil {
			servedRun.stop()
			<-servedRun.done
		}
		served.Store(nil)
		takenSaid.Store(false)
	})
	if w.Addr == gw {
		t.Fatalf("the page took the gateway's own address %s", gw)
	}
	// the gateway serves on its own address, and never the page's sign-in
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		res, err := http.Get("http://" + gw + "/")
		if err == nil {
			b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
			res.Body.Close()
			if strings.Contains(string(b), `"apis"`) {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the gateway on %s never served: %v", gw, err)
		}
	}
	// and the page answers where it went beside the gateway: the link's
	// key is traded for a sign-in cookie, which a plain Get wouldn't keep
	// past its redirect, so that is left unfollowed
	get := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := get.Get(w.Link)
	if err != nil || res.StatusCode != http.StatusSeeOther {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<10))
		res.Body.Close()
		t.Fatalf("the page on %s: %d %v %s", w.Link, res.StatusCode, err, b)
	}
	res.Body.Close()
}
