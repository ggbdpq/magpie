package gateway

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Huawei CodeArts / ModelArts MaaS returns HTTP 400 with
// InferHub.ModelArts.81011.403 when its content filter rejects a conversation
// (#7472). policyRefusal must recognize it (whether in error.message,
// error.code, or Huawei's raw error_code) so an order group fails over to the
// next member with failRefused and does not bench the account.
func TestCodeArtsContentFilter400FailsOverWithoutResting(t *testing.T) {
	const codeartsMsg = `{"error":{"type":"api_error","message":"CodeArts: 抱歉，我无法提供关于您提问内容的建议。作为 AI 编程助手，我更擅长协助您处理代码编写、代码调试、架构设计或技术实现。请告诉我您的项目中遇到了什么具体难题，我们继续讨论技术方案吧。 (InferHub.ModelArts.81011.403)"}}`
	const codeartsRaw = `{"error_code":"InferHub.ModelArts.81011.403","error_msg":"抱歉，我无法提供关于您提问内容的建议。"}`

	for _, c := range []struct {
		name  string
		reply string
		body  string
	}{
		{"plugin wrapped whole", codeartsMsg, `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`},
		{"plugin wrapped stream", codeartsMsg, `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`},
		{"raw modelarts whole", codeartsRaw, `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			a := &scripted{replies: []reply{{400, "", c.reply}}}
			b := &scripted{replies: []reply{{200, "", chatOK}}}
			scriptedOn(t, "a", provider.Chat, a)
			scriptedOn(t, "b", provider.Chat, b)
			if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
				t.Fatal(err)
			}
			s := New()
			code, body := sendTo(s, "/v1/chat/completions", c.body)
			if code != 200 || !strings.Contains(body, "hello") || strings.Contains(body, "81011.403") || a.n != 1 || b.n != 1 {
				t.Fatalf("%s: status=%d body=%s (a=%d b=%d)", c.name, code, body, a.n, b.n)
			}
			r := s.trace.routes[len(s.trace.routes)-1]
			if len(r.Tries) != 2 || r.Tries[0].Fail != failRefused || r.Tries[0].Rest != nil || r.Status != 200 {
				t.Fatalf("%s: tries=%+v status=%d", c.name, r.Tries, r.Status)
			}
			if s.resting("a") {
				t.Fatalf("%s: content-filter 400 rested provider a", c.name)
			}
		})
	}

	t.Run("unrelated 400 remains terminal", func(t *testing.T) {
		fresh(t)
		a := &scripted{replies: []reply{{400, "", `{"error":{"type":"invalid_request_error","message":"invalid temperature value"}}`}}}
		b := &scripted{replies: []reply{{200, "", chatOK}}}
		scriptedOn(t, "a", provider.Chat, a)
		scriptedOn(t, "b", provider.Chat, b)
		if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
			t.Fatal(err)
		}
		s := New()
		code, _ := sendTo(s, "/v1/chat/completions", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
		if code != 400 || a.n != 1 || b.n != 0 {
			t.Fatalf("unrelated 400 failed over: code=%d a=%d b=%d", code, a.n, b.n)
		}
	})
}
