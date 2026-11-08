package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/middleware"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/testenv"
)

// echoLast answers a chat completion whose content is the last message's
// content, so a test sees what the middleware made of the request.
func echoLast(w http.ResponseWriter, got []byte) {
	var sent struct {
		Messages []struct {
			Content any `json:"content"`
		} `json:"messages"`
	}
	json.Unmarshal(got, &sent)
	var last []byte
	if len(sent.Messages) == 0 {
		last, _ = json.Marshal("no messages in " + strings.ReplaceAll(string(got), `"`, "'"))
	} else {
		last, _ = json.Marshal(sent.Messages[len(sent.Messages)-1].Content)
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","content":`+string(last)+`},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
}

func postWithKey(t *testing.T, path, body, bearer string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	New().Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// replyContent is a chat completion reply's message content.
func replyContent(t *testing.T, body string) string {
	t.Helper()
	var got struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil || len(got.Choices) == 0 {
		t.Fatalf("unreadable reply: %s", body)
	}
	return got.Choices[0].Message.Content
}

// A gateway middleware is told which gateway key called, its id and name,
// never the secret, and a request with no key names none (#5463): with
// that, a middleware can route by the key.
func TestMiddlewareSeesCallerKey(t *testing.T) {
	var got []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		echoLast(w, got)
	}))
	defer up.Close()
	fresh(t) // nothing another test left on "fake" (#3273)
	t.Cleanup(plugin.Settle)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	mw := filepath.Join(t.TempDir(), "callerkey.middleware.js")
	os.WriteFile(mw, []byte(`
export function onRequest(body, ctx) {
  const m = body.messages[body.messages.length - 1]
  m.content += "@@@ " + JSON.stringify({ key: ctx.key ?? null, keyName: ctx.keyName ?? null })
  return body
}`), 0o644)
	b, _ := json.Marshal(plugin.List{Plugins: []plugin.Entry{{Spec: mw}}})
	os.MkdirAll(settings.Dir(), 0o755)
	os.WriteFile(filepath.Join(settings.Dir(), "plugins.json"), b, 0o644)
	middleware.Reload()
	defer middleware.Reload()

	keys, secrets := newCaller(t, "Mid key")
	const req = `{"model":"fake/m1","messages":[{"role":"user","content":"hi"}]}`

	code, body := postWithKey(t, "/v1/chat/completions", req, secrets[0])
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	want := `@@@ {"key":"` + keys[0].ID + `","keyName":"Mid key"}`
	content := replyContent(t, body)
	if !strings.Contains(content, want) {
		t.Fatalf("the middleware saw no caller key (want %q): %s", want, content)
	}
	if strings.Contains(content, secrets[0]) || strings.Contains(string(got), secrets[0]) {
		t.Fatalf("the key's secret reached the middleware or the provider: %s", content)
	}

	code, body = postWithKey(t, "/v1/chat/completions", req, "")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if !strings.Contains(replyContent(t, body), `@@@ {"key":null,"keyName":null}`) {
		t.Fatalf("a keyless request named a key: %s", body)
	}
}

// The plugins' gateway hooks are told the same: the calling key's id and
// name, never the secret (#5463).
func TestGatewayHooksSeeCallerKey(t *testing.T) {
	captured := make(chan map[string]any, 4)
	observer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		json.NewDecoder(r.Body).Decode(&payload)
		captured <- payload
		w.WriteHeader(http.StatusNoContent)
	}))
	defer observer.Close()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		echoLast(w, b)
	}))
	defer up.Close()
	fresh(t)
	t.Setenv("MAGPIE_BUN", testenv.Bun(t))
	t.Cleanup(plugin.Settle)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(t.TempDir(), "callerkey.hooks.js")
	script := `export async function onRequest(body,ctx){ctx.state.k=ctx.key??null;ctx.state.kn=ctx.keyName??null;return body}
export async function onResponse(body,ctx){await fetch(ctx.options.url,{method:"POST",body:JSON.stringify({key:ctx.state.k,keyName:ctx.state.kn}),signal:ctx.signal})}`
	if err := os.WriteFile(abs, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Add(t.Context(), abs); err != nil {
		t.Fatal(err)
	}
	if err := plugin.SetOptions(abs, map[string]any{"url": observer.URL, "gatewayTimeoutMs": 5000}); err != nil {
		t.Fatal(err)
	}

	keys, secrets := newCaller(t, "Hook key")
	const req = `{"model":"fake/m1","messages":[{"role":"user","content":"hi"}]}`
	collect := func() map[string]any {
		select {
		case p := <-captured:
			return p
		case <-time.After(10 * time.Second):
			t.Fatal("the completion hook never reported")
			return nil
		}
	}

	if code, body := postWithKey(t, "/v1/chat/completions", req, secrets[0]); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	p := collect()
	if p["key"] != keys[0].ID || p["keyName"] != "Hook key" {
		t.Fatalf("the hook saw no caller key: %+v", p)
	}

	if code, body := postWithKey(t, "/v1/chat/completions", req, ""); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	p = collect()
	if p["key"] != nil || p["keyName"] != nil {
		t.Fatalf("a keyless request named a key: %+v", p)
	}
}
