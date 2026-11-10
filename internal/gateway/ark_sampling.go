package gateway

import (
	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// arkKimiTemperature supplies the default Ark's K3.1 Pro needs with
// thinking off. Its Coding Plan rejects absent/null temperature in that
// mode (400 "only 0.6 is allowed"), although its thinking-on probe works
// without one. Run after extraBody so its thinking switch is respected;
// a temperature the user put there still wins. Verified on 2026-11-10.
func arkKimiTemperature(p provider.Provider, to provider.Protocol, body []byte) []byte {
	if provider.HostOf(p.Base(to)) != arkHost ||
		gjson.GetBytes(body, "model").String() != "kimi-k3.1-pro" ||
		gjson.GetBytes(body, "thinking.type").String() != "disabled" ||
		gjson.GetBytes(body, "temperature").Type != gjson.Null {
		return body
	}
	return withFields(body, map[string]any{"temperature": 0.6})
}
