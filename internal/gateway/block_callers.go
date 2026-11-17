package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

const callerCacheRefusal = "Gateway kernel rejected the request cache controls before dispatch (CRAG_CACHE_CONTROL)"

// retryCallerCache handles a custom relay's explicit pre-dispatch refusal
// (#6914). caller must stay: programmatic calls name their parent code
// execution there. Only the conflicting tool_use cache marks come out;
// this can shorten the cached prefix, but other breakpoints remain.
// Normal replies and other failures never change the request.
func (s *Server) retryCallerCache(ctx context.Context, p provider.Provider, to provider.Protocol, path string, body []byte, in http.Header, res *http.Response) (*http.Response, error) {
	if to != provider.Anthropic || res.StatusCode != http.StatusBadGateway || p.Preset != "" || p.Account != nil || p.PassErrors {
		return res, nil
	}
	fixed := withoutCallerCache(body)
	if bytes.Equal(fixed, body) {
		return res, nil
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		res.Body.Close()
		return nil, err
	}
	res.Body = readCloser{io.MultiReader(bytes.NewReader(b), res.Body), res.Body}
	if !bytes.Contains(b, []byte(callerCacheRefusal)) {
		return res, nil
	}
	res.Body.Close()
	return s.forwardOnce(ctx, p, to, path, fixed, in)
}

func withoutCallerCache(body []byte) []byte {
	if !bodyHas(body, `"caller"`) || !bodyHas(body, `"cache_control"`) {
		return body
	}
	var q map[string]json.RawMessage
	var messages []json.RawMessage
	if json.Unmarshal(body, &q) != nil || json.Unmarshal(q["messages"], &messages) != nil {
		return body
	}
	changed := false
	for i, raw := range messages {
		content := gjson.GetBytes(raw, "content")
		if !content.IsArray() {
			continue
		}
		var blocks []json.RawMessage
		if json.Unmarshal([]byte(content.Raw), &blocks) != nil {
			return body
		}
		marked := false
		for j, b := range content.Array() {
			if b.Get("type").String() != "tool_use" || !b.Get("caller").Exists() || !b.Get("cache_control").Exists() {
				continue
			}
			var block map[string]json.RawMessage
			if json.Unmarshal(blocks[j], &block) != nil {
				return body
			}
			delete(block, "cache_control")
			blocks[j], _ = json.Marshal(block)
			marked = true
		}
		if marked {
			var message map[string]json.RawMessage
			if json.Unmarshal(raw, &message) != nil {
				return body
			}
			message["content"], _ = json.Marshal(blocks)
			messages[i], _ = json.Marshal(message)
			changed = true
		}
	}
	if !changed {
		return body
	}
	q["messages"], _ = json.Marshal(messages)
	fixed, err := json.Marshal(q)
	if err != nil {
		return body
	}
	return fixed
}
