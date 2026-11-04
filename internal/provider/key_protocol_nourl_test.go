package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// A key made for one protocol, on a provider with no URL for it, is asked
// on the URLs the provider has (#1702): the gateway sends requests there,
// so a test of an endpoint asks there too — not "no key is on for this
// endpoint", which sent xtnntn (#4056) editing providers.json by hand for
// a key the gateway was answering with all along.
func TestTestAsksOnTheURLsItHas(t *testing.T) {
	detectHome(t)
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/chat/completions":
			io.WriteString(w, `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"length"}]}`)
		default:
			http.Error(w, `{"error":{"message":"Invalid URL (`+r.Method+" "+r.URL.Path+`)"}}`, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	p := Provider{ID: "relay", Name: "Relay", Key: "sk-relay", KeyProtocol: Anthropic, Chat: srv.URL + "/v1", Models: []string{"gpt-5.5"}}
	ctx := context.Background()
	t.Run("Test", func(t *testing.T) {
		rs := p.Test(ctx)
		if len(rs) != 1 {
			t.Fatalf("Test: %+v", rs)
		}
		if r := rs[0]; !r.OK || r.Protocol != Chat || !strings.Contains(r.Model, "gpt-5.5") {
			t.Fatalf("the chat endpoint is asked with the key as the gateway asks it: %+v", r)
		}
		mu.Lock()
		defer mu.Unlock()
		var posts []string
		for _, a := range seen {
			if strings.HasPrefix(a, "POST ") {
				posts = append(posts, a)
			}
		}
		if len(posts) != 1 || posts[0] != "POST /v1/chat/completions" {
			t.Fatalf("the upstream saw %v", posts)
		}
	})
	t.Run("TestModels", func(t *testing.T) {
		rs := p.TestModels(ctx, []string{"gpt-5.5"})
		if len(rs) != 1 {
			t.Fatalf("TestModels: %+v", rs)
		}
		if r := rs[0]; !r.OK || r.Protocol != Chat {
			t.Fatalf("a model's test asks the endpoint too: %+v", r)
		}
	})
}
