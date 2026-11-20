package gateway

import (
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
)

// The developer role a Chat client sent goes upstream as it came (#7256):
// an OpenAI-compatible backend that takes it keeps a developer turn's
// prompt cache apart from a system one's, and the swap magpie made of it
// cost a cache miss a turn. A backend that turns a developer turn away is
// asked once more with its messages as system (passthrough), and from then
// on given the swap from the start; DeepSeek's own API, which answers one
// 422 "unknown variant `developer`" (#1), is not refused even the first
// time.

// developerRoleRefusal is a Chat Completions upstream turning a developer
// message away in so many words: serde's "unknown variant `developer`"
// (DeepSeek's, #1), a validator's "developer is not one of ['system',
// 'assistant', …]" (#2955's wording, its Responses twin), a tag "does not
// match" any expected, the role "is not a valid role", or "is not
// supported". A fault over a developer message's size or content is
// another refusal and doesn't match; nor does one over the system
// messages a backend that takes developer alone turns away, which
// codex_backend.go's roleRefused reads.
var developerRoleRefusal = lazyre.New(`(?i)unknown variant ['"\x60]?developer['"\x60]?\b|['"\x60]?developer['"\x60]? is not one of |tag ['"\x60]?developer['"\x60]?[^.\n]{0,60}does not match|['"\x60]?developer['"\x60]? is not a valid role|['"\x60]?developer['"\x60]?(?: role| message)?(?: is| was)? not (?:supported|allowed|accepted|permitted)`)

// developerRoleRefused is how unfit remembers an upstream turning a
// developer turn away, per model: a relay may serve one model from a
// backend that takes the role and another from one that doesn't.
func developerRoleRefused(model string) string { return "developer role\x00" + model }

// chatTakesNoDeveloper reports whether p's Chat API is DeepSeek's own,
// which takes no developer turn (#1). Its shape is checked against the
// host, not the preset: a "deepseek" provider pointed at another host is
// that host's shape, and it learns from that endpoint's refusals as any
// other does (isDeepSeekAnthropic).
func chatTakesNoDeveloper(p provider.Provider) bool {
	return p.VendorHost() == deepseekHost
}
