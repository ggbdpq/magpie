package gateway

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
)

// A client that gzips the large bodies it sends (a proxy on the way to
// magpie does, at 16 KB and up) names the model in the packed body as any
// other: a name the key gives a model is swapped for the model in the
// unpacked body, which goes on unpacked, and a request the key gives no
// name for goes on exactly as it came, every packed byte of it.

const aliasWho = "default"

func aliasIdentity() access.Identity {
	return access.Identity{Aliases: map[string]string{aliasWho: "plan/m1"}}
}

func packed(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := gzip.NewWriter(&buf)
	if _, err := z.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func packedRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	b := packed(t, body)
	r := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Content-Encoding", "gzip")
	r.ContentLength = int64(len(b))
	return r
}

func readAll(t *testing.T, r *http.Request) string {
	t.Helper()
	if r.Body == nil {
		t.Fatal("no body")
	}
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const aliasChat = `{"model":"default","messages":[{"role":"user","content":"hi"}]}`

// gzip and a name the key gives: the body goes on unpacked, its model the
// model, and the headers say so.
func TestAliasUnpacksGzipBodyForAName(t *testing.T) {
	r := aliasBody(packedRequest(t, aliasChat), aliasIdentity())
	if enc := r.Header.Get("Content-Encoding"); enc != "" {
		t.Fatalf("Content-Encoding is still %q", enc)
	}
	got := readAll(t, r)
	want := `{"model":"plan/m1","messages":[{"role":"user","content":"hi"}]}`
	if got != want {
		t.Fatalf("body %s, want %s", got, want)
	}
	if r.ContentLength != int64(len(want)) {
		t.Fatalf("ContentLength %d, want %d", r.ContentLength, len(want))
	}
	if cl := r.Header.Get("Content-Length"); cl != strconv.Itoa(len(want)) {
		t.Fatalf("Content-Length %q, want %d", cl, len(want))
	}
}

// gzip and a model the key gives no name for: the packed bytes go on as
// they came, the header with them.
func TestAliasLeavesGzipBodyItHasNoNameFor(t *testing.T) {
	const body = `{"model":"plan/m2","messages":[{"role":"user","content":"hi"}]}`
	b := packed(t, body)
	r := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Content-Encoding", "gzip")
	r.ContentLength = int64(len(b))
	before := r.Header.Clone()

	r = aliasBody(r, aliasIdentity())
	if enc := r.Header.Get("Content-Encoding"); enc != "gzip" {
		t.Fatalf("Content-Encoding %q, want gzip", enc)
	}
	if want := before.Get("Content-Length"); r.Header.Get("Content-Length") != want {
		t.Fatalf("Content-Length %q, want %q", r.Header.Get("Content-Length"), want)
	}
	if r.ContentLength != int64(len(b)) {
		t.Fatalf("ContentLength %d, want %d", r.ContentLength, len(b))
	}
	got, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, b) {
		t.Fatalf("the body changed: %d bytes for the %d that came", len(got), len(b))
	}
	// and it is still gzip a route behind can read
	z, err := gzip.NewReader(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("not gzip any more: %v", err)
	}
	plain, err := io.ReadAll(z)
	if err != nil || string(plain) != body {
		t.Fatalf("unpacked %q, %v", plain, err)
	}
}

// a packed body larger than the limit, unpacked: it goes on packed, as it
// came, whatever its model.
func TestAliasLeavesGzipBodyOverTheLimit(t *testing.T) {
	body := `{"model":"default","messages":[{"role":"user","content":"` + strings.Repeat("x", 4096) + `"}]}`
	b := packed(t, body)
	r := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Content-Encoding", "gzip")
	r.ContentLength = int64(len(b))

	r = aliasPackedBodyTo(r, aliasIdentity(), 1024)
	if r.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding %q, want gzip", r.Header.Get("Content-Encoding"))
	}
	got, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, b) {
		t.Fatalf("the body changed: %d bytes for the %d that came", len(got), len(b))
	}
}

// a body that says gzip and isn't: it goes on as it came, and the route
// behind says what is wrong with it.
func TestAliasLeavesBrokenGzipBody(t *testing.T) {
	b := []byte(`{"model":"default","messages":[]}`)
	r := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Content-Encoding", "gzip")
	r.ContentLength = int64(len(b))

	r = aliasBody(r, aliasIdentity())
	if r.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding %q, want gzip", r.Header.Get("Content-Encoding"))
	}
	if got := readAll(t, r); got != string(b) {
		t.Fatalf("body %q, want %q", got, b)
	}
}

// a gzip header over a body that is cut short: the bytes that came go on
// as they came, so the route behind refuses it as it would have.
func TestAliasLeavesTruncatedGzipBody(t *testing.T) {
	b := packed(t, aliasChat)
	cut := b[:len(b)/2]
	r := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(cut))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Content-Encoding", "gzip")
	r.ContentLength = int64(len(cut))

	r = aliasBody(r, aliasIdentity())
	if got := readAll(t, r); !bytes.Equal([]byte(got), cut) {
		t.Fatalf("the body changed: %d bytes for the %d that came", len(got), len(cut))
	}
	if r.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding %q, want gzip", r.Header.Get("Content-Encoding"))
	}
}

// no Content-Encoding: the body is read, its model swapped, the length
// set, as it was before gzip was read here at all.
func TestAliasUnpackedBodyUnchanged(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(aliasChat))
	r.Header.Set("Content-Type", "application/json")
	r.ContentLength = int64(len(aliasChat))

	r = aliasBody(r, aliasIdentity())
	got := readAll(t, r)
	want := `{"model":"plan/m1","messages":[{"role":"user","content":"hi"}]}`
	if got != want {
		t.Fatalf("body %s, want %s", got, want)
	}
	if r.ContentLength != int64(len(want)) || r.Header.Get("Content-Length") != strconv.Itoa(len(want)) {
		t.Fatalf("length %d / %q, want %d", r.ContentLength, r.Header.Get("Content-Length"), len(want))
	}
}

// another encoding is not unpacked here: brotli, zstd and the rest go on
// as they came, the model named in them as it is.
func TestAliasLeavesOtherEncodings(t *testing.T) {
	for _, enc := range []string{"br", "zstd", "deflate", "dcz"} {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(aliasChat))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Content-Encoding", enc)
		r.ContentLength = int64(len(aliasChat))

		r = aliasBody(r, aliasIdentity())
		if got := r.Header.Get("Content-Encoding"); got != enc {
			t.Fatalf("%s: Content-Encoding %q", enc, got)
		}
		if got := readAll(t, r); got != aliasChat {
			t.Fatalf("%s: body %q", enc, got)
		}
	}
}

// a client that sent its body chunked keeps reading it whole when the body
// goes back as it came.
func TestAliasGivesBackChunkedGzipWhole(t *testing.T) {
	const body = `{"model":"plan/m2","messages":[{"role":"user","content":"hi"}]}`
	b := packed(t, body)
	r := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Content-Encoding", "gzip")
	r.ContentLength = -1
	r.TransferEncoding = []string{"chunked"}

	r = aliasBody(r, aliasIdentity())
	got, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, b) {
		t.Fatalf("the body changed: %d bytes for the %d that came", len(got), len(b))
	}
	if strings.Join(r.TransferEncoding, ",") != "chunked" {
		t.Fatalf("Transfer-Encoding %v", r.TransferEncoding)
	}
}

// the same request over a handler: a packed body asking by the key's name
// reaches the model, an unpacked one asks by its own name as it did.
func TestAliasGzipBodyOverTheGateway(t *testing.T) {
	fresh(t)
	plan := &fake{t: t, ctype: "application/json", reply: `{"id":"from-plan","choices":[]}`}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	twoProviders(t, plan, spare)
	keys, secrets := newCaller(t, "Packed")
	if _, err := access.Update("aliases-key", access.Change{Key: keys[0].ID, Models: []string{"plan/*"}, Aliases: map[string]string{"default": "plan/m1"}}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	packedDo := func(body string) *httptest.ResponseRecorder {
		b := packed(t, body)
		r := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(b))
		r.Header.Set("Authorization", "Bearer "+secrets[0])
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Content-Encoding", "gzip")
		r.ContentLength = int64(len(b))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	plan.calls = 0
	if w := packedDo(aliasChat); w.Code != 200 {
		t.Fatalf("packed body by name: %d %s", w.Code, w.Body.String())
	}
	if plan.calls != 1 || !strings.Contains(string(plan.got), `"m1"`) || strings.Contains(string(plan.got), "default") {
		t.Fatalf("the provider got %s", plan.got)
	}
	// a model the key gives no name for is sent as it was asked
	plan.calls = 0
	const plain = `{"model":"plan/m1","messages":[{"role":"user","content":"hi"}]}`
	if w := packedDo(plain); w.Code != 200 {
		t.Fatalf("packed body by model: %d %s", w.Code, w.Body.String())
	}
	if plan.calls != 1 || !strings.Contains(string(plan.got), `"m1"`) {
		t.Fatalf("the provider got %s", plan.got)
	}
}
