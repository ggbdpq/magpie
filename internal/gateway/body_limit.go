package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/yetone/magpie/internal/provider"
)

// A provider's own cap on the bytes of one request, as it goes out
// (issue #4282, kylefu8). A long session of images can pass a vendor's
// byte cap while its tokens still fit the model's window, and every turn
// of it is refused from then on; the images a plugin shrinks are no
// guarantee, a plugin that fails or times out standing aside. So the
// user sets a limit per provider, and forwardOnce measures the request
// after every plugin, route, translation and fitting has had its say —
// the bytes provider.SetBody hands the transport, the Content-Length on
// the wire. A body over the limit is refused with a 413 said here: the
// provider is asked nothing, the body is not cut, and the request is not
// sent again, fitted or otherwise. A turn to a Codex account goes
// zstd-compressed on the wire; the limit counts it before that
// compression, the plain bytes, which is the larger and so the safer
// side of what the vendor receives.

// bodyLimitMark marks, for the moment forward reads it, the 413 said
// here for a body over the provider's BodyLimit. It comes off at once:
// the vendor's learned byte cap (refitOverCap) would fit such a body
// smaller and ask again, and this request never went out.
const bodyLimitMark = "Magpie-Body-Limit"

// refusedForBodyLimit says whether res is the 413 said here for a body
// over the provider's BodyLimit, and takes its mark off.
func refusedForBodyLimit(res *http.Response) bool {
	if res == nil || res.Header.Get(bodyLimitMark) == "" {
		return false
	}
	res.Header.Del(bodyLimitMark)
	return true
}

// bodyLimitWords is what that 413 calls itself: the one phrase no vendor
// says, by which the refusal is known again where it lands (as
// restrictedSaid knows magpie's own words of a restricted account).
const bodyLimitWords = "the provider's body limit, `magpie provider body-limit`"

// bodyLimitSaid says a refusal is the 413 said here for a body over the
// provider's BodyLimit, read from its words. serve reads it to keep the
// refusal from being handed to another member: the limit is the user's
// own word for this provider, and the body never went out, so there is
// nothing of it for another to take.
func bodyLimitSaid(body []byte) bool {
	return bytes.Contains(body, []byte(bodyLimitWords))
}

// bodyLimitRefusal is that 413: the size, the limit, and where the limit
// is set, said as a request_too_large so a client that compacts on one
// (Codex) may do so.
func bodyLimitRefusal(p provider.Provider, size, limit int) *http.Response {
	msg := fmt.Sprintf("the request is %s, over the %s most %s takes in one request (%s); it was not sent", mb(size), mb(limit), p.Name, bodyLimitWords)
	out, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]any{"type": "request_too_large", "message": msg}})
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set(bodyLimitMark, "1")
	return &http.Response{
		Status: "413 Request Entity Too Large", StatusCode: http.StatusRequestEntityTooLarge,
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: h, Body: io.NopCloser(bytes.NewReader(out)), ContentLength: int64(len(out)),
	}
}
