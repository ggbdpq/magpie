package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/tidwall/gjson"
)

// #7448: a reused upstream Codex WebSocket whose TCP write path stalls must
// time out in k.c.Write, close the wedged socket, and retry the turn whole on
// a fresh WebSocket instead of hanging indefinitely until the caller cancels.
func TestCodexReusedSocketWriteTimeoutRetriesOnFreshSocket(t *testing.T) {
	socketOn(t)
	was := codexSocketDial
	codexSocketDial = 200 * time.Millisecond
	t.Cleanup(func() { codexSocketDial = was })

	unblock := make(chan struct{})
	t.Cleanup(func() { close(unblock) })

	var mu sync.Mutex
	handshakes := 0
	var turns []socketTurn
	srv := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/backend-api/codex/responses") {
			http.NotFound(w, r)
			return
		}
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "unexpected http fallback", http.StatusBadGateway)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		c.SetReadLimit(-1)
		mu.Lock()
		handshakes++
		n := handshakes
		mu.Unlock()

		_, msg, err := c.Read(r.Context())
		if err != nil {
			return
		}
		mu.Lock()
		turns = append(turns, socketTurn{header: r.Header.Clone(), msg: msg, socket: n})
		mu.Unlock()
		for _, fr := range []string{
			`{"type":"response.created","response":{"id":"resp-1"}}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_a","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"pong"}]}}`,
			`{"type":"response.completed","response":{"id":"resp-1","usage":{"input_tokens":7,"output_tokens":1}}}`,
		} {
			if c.Write(r.Context(), websocket.MessageText, []byte(fr)) != nil {
				return
			}
		}
		if n == 1 {
			// Stop reading on socket 1 so the reused write fills TCP buffers
			// and blocks inside FD.Write until codexSocketWrite expires.
			<-unblock
			return
		}
		c.Close(websocket.StatusNormalClosure, "")
	})
	chatgpt(t, srv)

	// Turn 1: establishes kept socket #1.
	keptPost(t, "acct-1", keptTurn("conv-hang", "gpt-5.5", userPing))

	// Build a large incompressible user message (~8 MB) so the WebSocket write
	// on wedged socket #1 blocks inside TCP Write even with permessage-deflate.
	raw := make([]byte, 4<<20)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	bigUser := `{"type":"message","role":"user","content":[{"type":"input_text","text":"` + hex.EncodeToString(raw) + `"}]}`
	body := keptTurn("conv-hang", "gpt-5.5", userPing, asstPong, bigUser)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer chatgpt-token-acct-1")
	req.Header.Set("chatgpt-account-id", "acct-1")
	New().Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "event: response.completed\n") {
		t.Fatalf("turn 2 failed (status=%d, ctxErr=%v): %s", rec.Code, ctx.Err(), rec.Body.String())
	}
	mu.Lock()
	gotHandshakes := handshakes
	gotTurns := append([]socketTurn(nil), turns...)
	mu.Unlock()
	if gotHandshakes != 2 || len(gotTurns) != 2 || gotTurns[1].socket != 2 {
		t.Fatalf("handshakes=%d turns=%d; want recovery on fresh socket #2", gotHandshakes, len(gotTurns))
	}
	if gjson.GetBytes(gotTurns[1].msg, "previous_response_id").Exists() {
		t.Fatalf("retry on fresh socket #2 should send whole turn without previous_response_id")
	}
}
