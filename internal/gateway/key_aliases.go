package gateway

import (
	"bytes"
	"compress/gzip"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/access"
)

// A gateway key may give models names of its own (access.Key.Aliases,
// tsingchao on X): /v1/models lists the name where the model was, and a
// request that asks by the name is the request for the model. The one
// place that happens is identifyCaller, before any route reads the model:
// the body's "model" of a JSON request, a Gemini call's model in its path,
// and /v1/models/<name>. Every route after it, and every check of the
// key's models, sees the real model, so the models the key may use hold
// the name as they hold the model. Replies say the real model.

// maxAliasBody is the largest JSON body read to find its model: a larger
// one goes on as it came, which asks the model by its real id only. A body
// that came gzip is held to it unpacked, and to it packed.
const maxAliasBody = 32 << 20

// aliasRequest is r with the model a key's name stands for in the model's
// place: r as it is when the key gives no names, or r asks none of them.
func aliasRequest(r *http.Request, who access.Identity) *http.Request {
	if len(who.Aliases) == 0 {
		return r
	}
	switch r.Method {
	case http.MethodGet:
		if id, ok := strings.CutPrefix(r.URL.Path, "/v1/models/"); ok {
			if real, ok := who.AliasFor(id); ok {
				return withPath(r, "/v1/models/"+real)
			}
		}
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		if call, ok := strings.CutPrefix(r.URL.Path, "/v1beta/models/"); ok {
			if i := strings.LastIndex(call, ":"); i > 0 {
				if real, ok := who.AliasFor(call[:i]); ok {
					return withPath(r, "/v1beta/models/"+real+call[i:])
				}
			}
			return r
		}
		return aliasBody(r, who)
	}
	return r
}

func withPath(r *http.Request, path string) *http.Request {
	u := url.URL{}
	if r.URL != nil {
		u = *r.URL
	}
	u.Path, u.RawPath = path, ""
	r = r.Clone(r.Context())
	r.URL = &u
	return r
}

// aliasBody swaps the model of a JSON body, every other byte as it was.
func aliasBody(r *http.Request, who access.Identity) *http.Request {
	if r.Body == nil || r.Body == http.NoBody {
		return r
	}
	// a form or a multipart body is no JSON; a client that says no type
	// sends it
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "" && !strings.HasSuffix(mt, "json") {
		return r
	}
	// a client that gzips the large bodies it sends, a proxy on the way to
	// magpie among them, names the model in the packed body as any other:
	// gzip is the one encoding a name is read through here
	if gzipped(r) {
		return aliasPackedBody(r, who)
	}
	if r.Header.Get("Content-Encoding") != "" {
		return r
	}
	orig := r.Body
	buf, err := io.ReadAll(io.LimitReader(orig, maxAliasBody+1))
	rest := io.MultiReader(bytes.NewReader(buf), orig)
	r.Body = struct {
		io.Reader
		io.Closer
	}{rest, orig}
	if err != nil || len(buf) > maxAliasBody {
		return r
	}
	real, ok := who.AliasFor(modelOf(buf))
	if !ok {
		return r
	}
	buf = rewriteModel(buf, real)
	r.Body = io.NopCloser(bytes.NewReader(buf))
	r.ContentLength = int64(len(buf))
	r.Header = r.Header.Clone()
	r.Header.Set("Content-Length", strconv.Itoa(len(buf)))
	r.GetBody = nil
	return r
}

// gzipped is whether the body came gzip, which is the encoding a proxy that
// packs the large bodies it forwards sets. "identity", which is no packing,
// and every other encoding go the way they went.
func gzipped(r *http.Request) bool {
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))) {
	case "gzip", "x-gzip":
		return true
	}
	return false
}

// aliasPackedBody is aliasBody for a body that came gzip: the body is
// unpacked to find its model, within maxAliasBody of unpacked bytes, and a
// name of the key's is swapped in the unpacked body, which goes on as it is,
// "Content-Encoding" taken off and the length set. A model that is no name of
// the key's, a body that can't be unpacked and one larger than maxAliasBody
// go on as they came, every packed byte of them, which the routes behind read
// through their own decoding.
func aliasPackedBody(r *http.Request, who access.Identity) *http.Request {
	return aliasPackedBodyTo(r, who, maxAliasBody)
}

// aliasPackedBodyTo is aliasPackedBody with the limit given, so that a test
// can hold a small body to a small one.
func aliasPackedBodyTo(r *http.Request, who access.Identity, limit int64) *http.Request {
	orig := r.Body
	// the packed bytes read so far: a body given back goes on with them, then
	// with the rest of itself, byte for byte as it came
	var packed bytes.Buffer
	z, err := gzip.NewReader(io.TeeReader(io.LimitReader(orig, limit+1), &packed))
	if err == nil {
		var plain []byte
		if plain, err = io.ReadAll(io.LimitReader(z, limit+1)); err == nil && int64(len(plain)) <= limit {
			if real, ok := who.AliasFor(modelOf(plain)); ok {
				plain = rewriteModel(plain, real)
				r.Body = io.NopCloser(bytes.NewReader(plain))
				r.ContentLength = int64(len(plain))
				r.Header = r.Header.Clone()
				r.Header.Del("Content-Encoding")
				r.Header.Set("Content-Length", strconv.Itoa(len(plain)))
				// the body is one piece now and says how long it is, so a client
				// that sent it chunked is not passed on as one
				r.TransferEncoding = nil
				r.GetBody = nil
				return r
			}
		}
	}
	r.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(packed.Bytes()), orig), orig}
	return r
}

// aliasNames puts the names a key gives models where the models were in a
// list /v1/models gives it.
func aliasNames(r *http.Request, data []map[string]any) {
	who := access.Caller(r.Context())
	if len(who.Aliases) == 0 {
		return
	}
	for _, m := range data {
		if id, _ := m["id"].(string); id != "" {
			if name, ok := who.AliasOf(id); ok {
				m["id"] = name
			}
		}
	}
}
