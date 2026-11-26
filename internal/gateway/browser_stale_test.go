package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/yetone/magpie/internal/browserext"
)

// A half-open extension connection that stops sending frames (frozen MV3
// service worker or sleep/wake) is dropped after browserReadTimeout and
// removed from the gateway's browser list, while a live connection that
// keeps pinging stays connected (#7177).
func TestBrowserDropsSilentHalfOpenExtension(t *testing.T) {
	fresh(t)
	prev := browserReadTimeout
	browserReadTimeout = 120 * time.Millisecond
	t.Cleanup(func() { browserReadTimeout = prev })

	s := New()
	srv := httptest.NewServer(lanGuard(s.Handler()))
	t.Cleanup(srv.Close)

	dial := func(agent string) (*websocket.Conn, context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		u := "ws" + strings.TrimPrefix(srv.URL, "http") + BrowserExtensionPath
		c, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{
			HTTPHeader: http.Header{"Origin": {browserext.Origin}},
		})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if err := c.Write(ctx, websocket.MessageText, []byte(`{"type":"hello","version":"`+browserext.Version()+`","agent":"`+agent+`"}`)); err != nil {
			cancel()
			c.CloseNow()
			t.Fatal(err)
		}
		return c, ctx, cancel
	}

	// Silent (half-open) extension: sends hello, then nothing.
	silent, _, silentCancel := dial("Chrome/157")
	t.Cleanup(func() { silentCancel(); silent.CloseNow() })
	browserConnected(t, s)

	// Live extension: sends pings faster than browserReadTimeout and reads pongs.
	live, liveCtx, liveCancel := dial("Edge/157")
	liveDone := make(chan struct{})
	t.Cleanup(func() { liveCancel(); live.CloseNow(); <-liveDone })
	go func() {
		defer close(liveDone)
		tick := time.NewTicker(40 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-liveCtx.Done():
				return
			case <-tick.C:
				if live.Write(liveCtx, websocket.MessageText, []byte(`{"type":"ping"}`)) != nil {
					return
				}
				rctx, rcancel := context.WithTimeout(liveCtx, 200*time.Millisecond)
				_, _, err := live.Read(rctx)
				rcancel()
				if err != nil {
					return
				}
			}
		}
	}()

	// Wait until the gateway has both connections registered, then wait for
	// the silent connection to be reaped while the pinging one remains.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		all := s.browser.all("")
		if len(all) == 1 && strings.Contains(all[0].agent, "Edge/157") {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	var agents []string
	for _, c := range s.browser.all("") {
		agents = append(agents, c.id+":"+c.agent)
	}
	t.Fatalf("expected silent Chrome/157 to be dropped and live Edge/157 to remain, got %v", agents)
}
