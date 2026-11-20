package gateway

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #7256: the developer role a Chat client sent goes upstream as it came.
// An OpenAI-compatible backend that takes the role keeps a developer
// turn's prompt cache apart from a system one's, and the swap magpie made
// of it cost a cache miss a turn. Only a backend that turns a developer
// turn away is given the swap: DeepSeek's own API from the start (its 422
// "unknown variant `developer`" left Pi with no reply, #1), any other one
// it learns from a refusal of its own wording.

// Pi's developer turn to an OpenAI-compatible upstream goes as it was
// sent, system nowhere in it.
func TestChatPassthroughKeepsDeveloper(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", reply: `{"id":"c1","choices":[]}`}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1/chat/completions", `{"model":"m1","reasoning_effort":"high","messages":[{"role":"developer","content":"be brief"},{"role":"user","content":"a developer asks"}]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if !bytes.Contains(f.got, []byte(`"role":"developer"`)) || bytes.Contains(f.got, []byte(`"role":"system"`)) {
		t.Errorf("forwarded body: %s", f.got)
	}
}

// DeepSeek's own API turned Pi's developer turns away for good (422
// "unknown variant `developer`", #1): to it they go as system from the
// start, in one call. The fake is reached through the provider's proxy so
// the provider's host is DeepSeek's own.
func TestChatPassthroughDeveloperAsSystemForDeepSeek(t *testing.T) {
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	f := &fake{t: t, ctype: "application/json", reply: `{"id":"c1","choices":[]}`}
	up := setup(t, provider.Chat, f)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"},
		Chat: "http://api.deepseek.com/v1", Proxy: up.URL}); err != nil {
		t.Fatal(err)
	}
	code, body := post(t, "/v1/chat/completions", `{"model":"m1","reasoning_effort":"high","messages":[{"role":"developer","content":"be brief"},{"role":"user","content":"a developer asks"}]}`)
	if code != 200 || f.calls != 1 {
		t.Fatalf("%d after %d calls: %s", code, f.calls, body)
	}
	if !bytes.Contains(f.got, []byte(`{"content":"be brief","role":"system"}`)) || bytes.Contains(f.got, []byte(`"role":"developer"`)) {
		t.Errorf("forwarded body: %s", f.got)
	}
}

// A backend that turns a developer turn away in its own words is asked
// once more with its messages as system, and from then on given the swap
// from the start; a refusal of another wording is the caller's to see.
func TestChatPassthroughDeveloperRefusedRetriedAsSystem(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", reply: `{"id":"c1","choices":[]}`}
	f.refuse = func(b []byte) (int, string) {
		if bytes.Contains(b, []byte(`"role":"developer"`)) {
			// DeepSeek's, per #1 (422 unknown variant `developer`)
			return http.StatusUnprocessableEntity, `{"error":{"message":"unknown variant ` + "\x60developer\x60" + `, expected one of ` + "\x60system\x60, \x60user\x60, \x60assistant\x60, \x60tool\x60" + `","type":"invalid_request_error"}}`
		}
		return 0, ""
	}
	setup(t, provider.Chat, f)
	srv := New()
	code, body := postTo(t, srv, "/v1/chat/completions", `{"model":"m1","messages":[{"role":"developer","content":"be brief"},{"role":"user","content":"a developer asks"}]}`)
	if code != 200 || f.calls != 2 {
		t.Fatalf("%d after %d calls: %s", code, f.calls, body)
	}
	if bytes.Contains(f.got, []byte(`"role":"developer"`)) || !bytes.Contains(f.got, []byte(`{"content":"be brief","role":"system"}`)) {
		t.Errorf("asked again as %s", f.got)
	}
	// from then on, the swap from the start
	f.calls = 0
	if code, body = postTo(t, srv, "/v1/chat/completions", `{"model":"m1","messages":[{"role":"developer","content":"be brief"}]}`); code != 200 || f.calls != 1 {
		t.Fatalf("next turn: %d after %d calls: %s", code, f.calls, body)
	}
	if bytes.Contains(f.got, []byte(`"role":"developer"`)) {
		t.Errorf("next turn sent as %s", f.got)
	}
}

// A refusal of the developer role is known however a validator words it; a
// fault over something else is not taken for one.
func TestDeveloperRoleRefusalWordings(t *testing.T) {
	for _, body := range []string{
		`{"error":{"message":"unknown variant ` + "\x60developer\x60" + `, expected one of ` + "\x60system\x60, \x60user\x60" + `"}}`,
		`{"error":{"message":"developer is not one of ['system', 'assistant', 'user', 'tool', 'function']"}}`,
		`{"error":{"message":"Input tag 'developer' found using 'type' does not match any of the expected tags"}}`,
		`{"error":{"message":"'developer' is not a valid role"}}`,
		`{"error":{"message":"role 'developer' is not supported"}}`,
		`{"error":{"message":"developer role is not allowed"}}`,
	} {
		if !developerRoleRefusal.MatchString(body) {
			t.Errorf("not a developer-role refusal: %s", body)
		}
	}
	for _, body := range []string{
		`{"error":{"message":"system messages are not allowed"}}`,
		`{"error":{"message":"developer message is too long"}}`,
		`{"error":{"message":"unknown variant ` + "\x60assistant\x60" + `"}}`,
		`{"error":{"message":"Unknown parameter: 'store'.","type":"invalid_request_error"}}`,
	} {
		if developerRoleRefusal.MatchString(body) {
			t.Errorf("taken for a developer-role refusal: %s", body)
		}
	}
}
