package gateway

import (
	"github.com/yetone/magpie/internal/lazyre"
	"strings"
)

const geminiCallFailurePrefix = "Gemini could not complete the tool call ("

func geminiCallFailureCode(code string) bool {
	return code == "malformed_function_call" || code == "unexpected_tool_call" || code == "invalid_deferred_tool_call"
}

// Match only our generated diagnostic, not arbitrary tool output or vendor text.
func geminiCallFailureTextOnly(text string) bool {
	return strings.Contains(text, "Gemini deferred tool call rejected: ") || strings.Contains(text, geminiCallFailurePrefix+"MALFORMED_FUNCTION_CALL)") ||
		strings.Contains(text, geminiCallFailurePrefix+"UNEXPECTED_TOOL_CALL)")
}

var geminiFailedCallName = lazyre.New(`call:default_api:([A-Za-z_][A-Za-z0-9_.-]{0,127})\{`)

// Google's diagnostic can contain the entire rejected call, including secrets
// and text-call markers. Expose only its bounded tool name, never its arguments.
func geminiCallFailureText(reason, detail string) string {
	text := geminiCallFailurePrefix + reason + ")"
	if m := geminiFailedCallName.FindStringSubmatch(detail); len(m) == 2 {
		text += "; tool " + m[1]
	}
	return text + ". Use smaller tool arguments or another model."
}
