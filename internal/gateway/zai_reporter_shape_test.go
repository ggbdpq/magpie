package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// zaiSevered serves the reply #3346 reports: glm-5.3 reasoning chunks over
// a 200 stream, the upstream's last write cut inside a data line under a
// lying Content-Length — the wire shape of "connection reset by peer", no
// network. The handler returns after the partial write, so the gateway's
// read fails mid-reply.
func zaiSevered(t *testing.T, events ...string) {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		hj, ok := w.(http.Hijacker)
		if !ok {
			panic("no hijacker")
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			panic(err)
		}
		defer conn.Close()
		body := strings.Join(events, "")
		fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: %d\r\n\r\n", len(body)+100)
		io.WriteString(buf, body)
		buf.Flush()
	}))
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "zai", Name: "Z.ai", Key: "k", Models: []string{"m1"}, Chat: up.URL + "/v1"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

// The shape #3346 reports for Z.ai's glm-5.3: the upstream answers a 200
// stream, then the connection dies mid-line. What reaches the client must
// be events it can read — no half JSON — the failure said in its own
// event, and the stream terminated so the agent can retry.
func TestZaiMidReplyResetShape(t *testing.T) {
	fresh(t)
	zaiSevered(t,
		`data: {"id":"202610220001118dfb145dcf634908","model":"glm-5.3","choices":[{"index":0,"delta":{"reasoning_content":"Attempt3 got past CF (422) but wrong ToS field name."}}]}`+"\n\n",
		`data: {"id":"202610220001118dfb145dcf634908","model":"glm-5.3","cho`)
	code, body := post(t, "/v1/chat/completions", `{"model":"zai/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status = %d, want 200; body: %s", code, body)
	}
	readSSE(strings.NewReader(body), func(_, data string) error {
		if data != "[DONE]" && !json.Valid([]byte(data)) {
			t.Fatalf("the severed stream left an event no client can read: %q", data)
		}
		return nil
	})
	i := strings.Index(body, "Z.ai: connection lost mid-reply")
	if i < 0 {
		t.Fatalf("the severed stream ended without the failure said: %s", body)
	}
	if !strings.Contains(body[i:], "data: [DONE]\n\n") {
		t.Fatalf("the error event was not followed by [DONE]: %s", body[i:])
	}
}
