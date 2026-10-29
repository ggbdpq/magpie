package provider

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/yetone/magpie/internal/catalog"
)

// A model whose API takes a reply's length as max_completion_tokens alone
// turns max_tokens away by name (#4261: gpt-6.1-sol on api.openai.com
// answers 400 "Unsupported parameter: 'max_tokens' is not supported with
// this model. Use 'max_completion_tokens' instead."). A test request, the
// smallest one, names max_tokens as Bedrock's and Azure's need not
// (tinyBody); asked again under the name the model takes, as the gateway
// asks such a chat again, it is answered.

// asCompletionTokens names a request's reply length as
// max_completion_tokens in place of max_tokens, as the gateway asks a chat
// request of such a model.
func asCompletionTokens(body []byte) []byte {
	var m map[string]any
	if json.Unmarshal(body, &m) != nil {
		return body
	}
	n, ok := m["max_tokens"]
	if !ok {
		return body
	}
	delete(m, "max_tokens")
	if _, has := m["max_completion_tokens"]; !has {
		m["max_completion_tokens"] = n
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// RetryCompletionTokens hands on res, the answer to req; or, when the
// vendor refused max_tokens for its model (CompletionTokensRefused), the
// answer to req asked again with the reply's length named as
// max_completion_tokens. do sends a request as req was sent.
func RetryCompletionTokens(req *http.Request, res *http.Response, do func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	if res == nil || res.StatusCode != http.StatusBadRequest || req.GetBody == nil {
		return res, nil
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	res.Body.Close()
	res.Body = io.NopCloser(bytes.NewReader(b))
	if !catalog.CompletionTokensRefused(res.StatusCode, b) {
		return res, nil
	}
	sent, err := req.GetBody()
	if err != nil {
		return res, nil
	}
	was, _ := io.ReadAll(sent)
	nb := asCompletionTokens(was)
	if len(nb) == 0 || bytes.Equal(nb, was) {
		return res, nil
	}
	again := req.Clone(req.Context())
	again.Body = io.NopCloser(bytes.NewReader(nb))
	again.ContentLength = int64(len(nb))
	return do(again)
}
