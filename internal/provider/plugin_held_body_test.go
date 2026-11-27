package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/testenv"
)

// HeldBody is the bytes SetBody gave whole, and none of another reader's
// or a let-go request's (#7596).
func TestHeldBody(t *testing.T) {
	var b = []byte(`{"model":"m","messages":[]}`)
	req, _ := http.NewRequest("POST", "http://x/v1", nil)
	SetBody(req, b)
	got := HeldBody(req)
	if string(got) != string(b) || &got[0] != &b[0] {
		t.Fatalf("held %q, want the very bytes", got)
	}
	LetGo(req)
	if HeldBody(req) != nil {
		t.Fatal("held after LetGo")
	}
	other, _ := http.NewRequest("POST", "http://x/v1", strings.NewReader("hi"))
	if HeldBody(other) != nil {
		t.Fatal("another reader's body held")
	}
	if HeldBody(nil) != nil {
		t.Fatal("nil request held")
	}
}

// A plugin's fetch reads the gateway's own bytes rather than a second
// copy of a whole request body (#7596): the bytes reach the vendor as
// they were sent, and req's body is left for its GetBody.
func TestPluginFetchReadsTheHeldBody(t *testing.T) {
	bun := testenv.Bun(t)
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	var mu sync.Mutex
	var got []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = b
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	t.Cleanup(up.Close)
	t.Setenv("FAKE_BASE", up.URL)
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	k, err := plugin.Import(ctx, "fakeco", map[string]any{"type": "oauth", "refresh": "r-ok", "access": "a", "expires": 9e15, "accountId": "held@fake"})
	if err != nil {
		t.Fatal(err)
	}
	saveLogins(t, savedLogin{Agent: "plugin:fakeco", User: "held@fake", Home: k, On: true})
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	pp := mustPlugin(t)
	const sent = `{"model":"fake-claude","messages":[{"role":"user","content":"held body"}]}`
	req, _ := http.NewRequestWithContext(ctx, "POST", pluginBase+pp.ID+"/v1/chat/completions", nil)
	SetBody(req, []byte(sent))
	resp, err := pluginFetch(pp, k, req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	mu.Lock()
	defer mu.Unlock()
	if string(got) != sent {
		t.Fatalf("the vendor got %q, want what was sent", got)
	}
}
