package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// chiyan171 (#6914): the private relay refuses a tool_use carrying both
// caller and cache_control, before dispatch. Ordinary Anthropic endpoints
// accept the pair; programmatic calls still need their caller provenance.
func TestCallerCacheControlRefusal(t *testing.T) {
	for _, tc := range []struct {
		name      string
		rejection string
		pass      bool
		want      int
		calls     int
	}{
		{"accepts", "", false, 200, 1},
		{"CRAG refusal", "Gateway kernel rejected the request cache controls before dispatch (CRAG_CACHE_CONTROL)", false, 200, 2},
		{"programmatic CRAG refusal", "Gateway kernel rejected the request cache controls before dispatch (CRAG_CACHE_CONTROL)", false, 200, 2},
		{"other 502", "upstream temporarily unavailable", false, 502, 0},
		{"pass errors", "Gateway kernel rejected the request cache controls before dispatch (CRAG_CACHE_CONTROL)", true, 502, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			var mu sync.Mutex
			var bodies [][]byte
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				bodies = append(bodies, b)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				block := gjson.GetBytes(b, "messages.1.content.0")
				if strings.HasSuffix(tc.name, "CRAG refusal") && block.Get("caller").Exists() && block.Get("cache_control").Exists() {
					w.WriteHeader(502)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": tc.rejection}})
					return
				}
				if tc.name == "other 502" || tc.name == "pass errors" {
					w.WriteHeader(502)
					msg := tc.rejection
					if tc.name == "pass errors" {
						msg = callerCacheRefusal
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": msg}})
					return
				}
				io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
			}))
			t.Cleanup(up.Close)
			p := provider.Provider{ID: "crag", Name: "CRAG", Key: "k", Anthropic: up.URL, Models: []string{"claude-sonnet-5-5"}}
			if tc.pass {
				p.PassErrors = true
			}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			body := `{"model":"crag/claude-sonnet-5-5","max_tokens":128,"system":[{"type":"text","text":"rules","cache_control":{"type":"ephemeral"}}],"tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"read"},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"read","caller":{"type":"direct"},"input":{"caller":"argument"},"cache_control":{"type":"ephemeral"}},{"type":"text","text":"reading","cache_control":{"type":"ephemeral"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}]}`
			caller := `{"type":"direct"}`
			if tc.name == "programmatic CRAG refusal" {
				caller = `{"type":"code_execution_20260120","tool_id":"srvtoolu_parent"}`
				body = strings.Replace(body, `{"type":"direct"}`, caller, 1)
			}
			gw := httptest.NewServer(New().Handler())
			t.Cleanup(gw.Close)
			res, err := http.Post(gw.URL+"/v1/messages", "application/json", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			reply, err := io.ReadAll(res.Body)
			res.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if res.StatusCode != tc.want {
				t.Fatalf("status %d, want %d: %s", res.StatusCode, tc.want, reply)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(bodies) == 0 {
				t.Fatal("no request reached the upstream")
			}
			if tc.calls > 0 && len(bodies) != tc.calls {
				t.Fatalf("%d upstream calls, want %d", len(bodies), tc.calls)
			}
			for i, b := range bodies {
				block := gjson.GetBytes(b, "messages.1.content.0")
				if block.Get("caller.type").String() != gjson.Get(caller, "type").String() || block.Get("caller.tool_id").String() != gjson.Get(caller, "tool_id").String() || block.Get("input.caller").String() != "argument" || block.Get("id").String() != "toolu_1" {
					t.Errorf("try %d changed the tool call: %s", i, block.Raw)
				}
				wantMark := !(strings.HasSuffix(tc.name, "CRAG refusal") && i == 1)
				if block.Get("cache_control").Exists() != wantMark {
					t.Errorf("try %d cache mark present=%v, want %v", i, block.Get("cache_control").Exists(), wantMark)
				}
				if !gjson.GetBytes(b, "system.0.cache_control").Exists() || !gjson.GetBytes(b, "messages.1.content.1.cache_control").Exists() {
					t.Errorf("try %d removed unrelated cache marks: %s", i, b)
				}
			}
		})
	}
}

// Inspecting a refusal must neither consume the rest of a large response
// nor hide a failed read. The retry decision is below the HTTP handler,
// where these transport-level failures can be injected deterministically.
func TestCallerCacheRefusalBodyRead(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","caller":{"type":"direct"},"cache_control":{"type":"ephemeral"}}]}]}`)
	t.Run("large unrelated body stays readable and closable", func(t *testing.T) {
		original := strings.Repeat("x", (1<<20)+100)
		closed := false
		res := &http.Response{StatusCode: 502, Body: callerCacheTestBody{Reader: strings.NewReader(original), closed: &closed}}
		got, err := New().retryCallerCache(context.Background(), provider.Provider{}, provider.Anthropic, "/v1/messages", body, nil, res)
		if err != nil || got != res {
			t.Fatalf("unrelated response replaced: %v", err)
		}
		b, err := io.ReadAll(got.Body)
		if err != nil || !bytes.Equal(b, []byte(original)) {
			t.Fatalf("body changed: bytes=%d err=%v", len(b), err)
		}
		got.Body.Close()
		if !closed {
			t.Fatal("original body was not closed")
		}
	})
	t.Run("read error survives", func(t *testing.T) {
		closed := false
		res := &http.Response{StatusCode: 502, Body: callerCacheTestBody{Reader: callerCacheErrorReader{}, closed: &closed}}
		got, err := New().retryCallerCache(context.Background(), provider.Provider{}, provider.Anthropic, "/v1/messages", body, nil, res)
		if got != nil || !errors.Is(err, io.ErrUnexpectedEOF) || !closed {
			t.Fatalf("got response=%v error=%v closed=%v", got, err, closed)
		}
	})
}

type callerCacheTestBody struct {
	io.Reader
	closed *bool
}

func (b callerCacheTestBody) Close() error { *b.closed = true; return nil }

type callerCacheErrorReader struct{}

func (callerCacheErrorReader) Read(b []byte) (int, error) {
	return copy(b, callerCacheRefusal), io.ErrUnexpectedEOF
}

func TestWithoutCallerCacheOnlyDropsMatchingToolMark(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","caller":{"type":"direct"},"cache_control":{"type":"ephemeral"}},{"type":"server_tool_use","caller":{"type":"direct"},"cache_control":{"type":"ephemeral"}},{"type":"tool_use","input":{"caller":"arg"},"cache_control":{"type":"ephemeral"}}]}]}`)
	got := withoutCallerCache(body)
	if !gjson.GetBytes(got, "messages.0.content.0.caller").Exists() || gjson.GetBytes(got, "messages.0.content.0.cache_control").Exists() {
		t.Fatalf("matching tool_use mark not narrowly removed: %s", got)
	}
	if !gjson.GetBytes(got, "messages.0.content.1.cache_control").Exists() || !gjson.GetBytes(got, "messages.0.content.2.cache_control").Exists() {
		t.Fatalf("unrelated marks removed: %s", got)
	}
}
