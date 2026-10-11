package plugin

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// refuseAll is an HTTP proxy that answers every CONNECT with 502 and
// keeps every host it was asked for: one that carries nothing, so a
// request sent to it is told apart from one that went direct.
func refuseAll(t *testing.T) (string, func() []string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var asked []string
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				var head []byte
				b := make([]byte, 1)
				for !strings.HasSuffix(string(head), "\r\n\r\n") {
					if _, err := c.Read(b); err != nil {
						return
					}
					head = append(head, b[0])
				}
				f := strings.Fields(string(head))
				if len(f) < 2 {
					fmt.Fprint(c, "HTTP/1.1 400 Bad Request\r\n\r\n")
					return
				}
				mu.Lock()
				asked = append(asked, f[1])
				mu.Unlock()
				// a CONNECT is answered 502; an http:// target a proxy was
				// given comes as an absolute-form request, which nothing
				// here carries either
				fmt.Fprint(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
			}(c)
		}
	}()
	return "http://" + ln.Addr().String(), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(asked)
	}
}

// A host on the user's own network is reached directly, whatever proxy
// the host was started with, as magpie's own requests take it
// (netproxy.Func, #982): a plugin provider pointing at a gateway of the
// user's own — a Remote magpie, an LLM relay on the NAS — went through
// the proxy, which either can't reach it or can't resolve its name.
func TestPluginFetchLeavesTheUserNetworkDirect(t *testing.T) {
	sandbox(t)
	for _, k := range []string{"NO_PROXY", "no_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(k, "")
	}
	proxy, askedFor := refuseAll(t)
	t.Setenv("HTTPS_PROXY", proxy)
	t.Setenv("HTTP_PROXY", proxy)
	Restart()
	// the fetch fails fast on the old code (the proxy answers at once) and
	// on the new one it dials an address that answers nothing, which a
	// machine on the user's network bounces in a few seconds; a dead line
	// under a loaded one is bounded here
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	abs, _ := filepath.Abs("testdata/fake/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := APIKey(ctx, "fakeco", 0, nil, "k1", NewAccount); err != nil {
		t.Fatal(err)
	}
	// an address of the user's own network nothing answers on: what says
	// which way the fetch went is who was asked for it
	_, err := Fetch(ctx, FetchRequest{Provider: "fakeco", Model: "fake-1", NPM: "@ai-sdk/openai-compatible",
		URL: "http://192.168.255.254:9/v1/chat/completions", Method: "POST", Body: []byte(`{}`)})
	for _, a := range askedFor() {
		if strings.Contains(a, "192.168.255.254") {
			t.Fatalf("the proxy was asked to carry %s, a host on the user's own network; magpie's own requests leave it direct (asked: %v)", a, askedFor())
		}
	}
	if err != nil && strings.Contains(err.Error(), "proxyconnect") {
		t.Fatalf("a host on the user's own network went through the proxy: %v", err)
	}
}
