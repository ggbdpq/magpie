package library

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The hub marks each of magpie's own servers (a `magpie mcp …` command)
// as its own, so the gateway bounds its call by the caller's client alone
// (#6885); a server from elsewhere in the Library isn't marked.
func TestHubMarksMagpieOwnServers(t *testing.T) {
	hubSandbox(t)
	var seen http.Header
	srv := httptest.NewServer(mcpHandler(t, false, &seen))
	t.Cleanup(srv.Close)
	own := fakeStdio(t, "ok", nil)
	own.Name, own.Args, own.Agents = selfServerName, []string{"mcp", "image"}, []string{"claude"}
	ok(t)(SaveServer("", *own))
	other := fakeStdio(t, "ok", nil)
	other.Agents = []string{"claude"}
	ok(t)(SaveServer("", Server{Name: "web", Transport: "http", URL: srv.URL, Agents: []string{"claude"}}))
	ok(t)(SetMCPHub(true))
	hub := theHub{}
	for _, x := range hub.Tools(context.Background(), "claude") {
		if x.Own != (x.Server == selfServerName) {
			t.Errorf("%s/%s: own %v", x.Server, x.Tool, x.Own)
		}
	}
	if !isOwn(&Server{Transport: "stdio", Command: "magpie", Args: []string{"mcp", "search"}}) {
		t.Error("magpie's own search isn't marked")
	}
	if isOwn(&Server{Transport: "http", URL: "http://127.0.0.1:1"}) {
		t.Error("a remote server is marked")
	}
	if isOwn(&Server{Transport: "stdio", Command: "npx", Args: []string{"-y", "server-thing"}}) {
		t.Error("another tool's server is marked")
	}
}
