package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"slices"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// openCodeSeed names the conversation a request to OpenCode's gateway is a
// turn of, for its x-opencode-session (provider.OpenCodeClient): the
// client's own x-opencode-session when it sent one, else the session the
// client names (X-Magpie-Session, Codex's session_id, Claude Code's
// session, Pi's affinity: sessionOf), else the body's prompt_cache_key
// (Codex's thread), else the conversation's first user message
// (conversationID). OpenCode itself sends one session id for every turn of
// a session (packages/core/src/session/affinity.ts), and Zen and Go route
// a session to the backend that has its prompt cached: every turn of one
// conversation names the same, and two conversations never one. Without
// the cache key, two of Codex's conversations in one folder, which open on
// the same environment message, were one session.
func openCodeSeed(in http.Header, body []byte) string {
	if v := strings.TrimSpace(in.Get("x-opencode-session")); v != "" {
		return v
	}
	if v := sessionOf(in); v != "" {
		return v
	}
	if k := strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String()); k != "" {
		return "prompt_cache_key\x00" + k
	}
	return conversationID(in, body)
}

// logOpenCodeRefusal writes to magpie's log what went to OpenCode's
// gateway with a request it refused: where, the names of the headers sent
// (never a value), and whether x-opencode-session was among them, by its
// first 8 characters. OpenCode answers a request without one 400
// "Request is missing x-opencode-session and cannot be routed
// efficiently" (luci on Discord), and this tells that apart from a
// session the user's own header pins every conversation to.
func logOpenCodeRefusal(p provider.Provider, req *http.Request, status int) {
	var names []string
	for k := range req.Header {
		names = append(names, strings.ToLower(k))
	}
	slices.Sort(names)
	session := "missing"
	if v := req.Header.Get("x-opencode-session"); v != "" {
		session = v[:min(len(v), 8)] + "…"
		for k := range p.Headers {
			if strings.EqualFold(k, "x-opencode-session") {
				session += " (the provider's own header: one session for every conversation)"
			}
		}
	}
	log.Printf("opencode: %s answered %d to %s %s%s; x-opencode-session %s; headers sent: %s",
		p.ID, status, req.Method, req.URL.Host, req.URL.Path, session, strings.Join(names, ", "))
}

// errorKeys are the fields any error an API answers carries, in its own
// body or in an error object of it: a body none of them name says nothing
// of why.
var errorKeys = []string{"error", "message", "msg", "code", "type", "param", "detail", "reason", "description"}

// openCodeBareRefusal is res as the 502 it stands for, when OpenCode's
// gateway answers a request with a 400 whose body is no error at all —
// {"model":"deepseek-v4.1-flash"}, the model's name and nothing else
// (yetone/magpie#3740, s-leeq): the gateway's own echo, from a backend
// that turned the turn away before the model was asked, nothing in or out
// and no reply id. The same request is answered moments later (s-leeq's
// timeline: four 400s over 13s, then a 200 of the same prompt, its prefix
// cached by then), so it is a fault of a moment, and is read as the 502
// such a fault is: a group's next member is asked, the last one left is
// asked again after a pause, and the caller is told 502, which a caller's
// own retries retry, rather than a 400 that says the request was at
// fault, which nothing retries — the caller's turn just ended on it. A
// 400 that names anything, as the missing x-opencode-session refusal
// does, is an error, and goes on as it came.
func openCodeBareRefusal(req *http.Request, res *http.Response) *http.Response {
	if res == nil || res.StatusCode != http.StatusBadRequest || res.Body == nil ||
		req == nil || !provider.OpenCodeURL(req.URL.String()) {
		return res
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	res.Body.Close()
	res.Body = io.NopCloser(bytes.NewReader(b))
	var v map[string]any
	if json.Unmarshal(bytes.TrimSpace(b), &v) != nil {
		return res
	}
	for _, k := range errorKeys {
		if _, ok := v[k]; ok {
			return res
		}
	}
	return asBadGateway(res, "", "answered "+res.Status+" with "+clipRunes(strings.TrimSpace(string(b)), 200)+", which is no error, not an API reply")
}
