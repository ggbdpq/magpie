package davsync

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Apache's ap_make_etag makes a weak ETag (W/"…") for a GET that lands in
// the same second as the file's mtime — even uncompressed (#1362 asked
// identity for the compression face). A weak ETag never matches an
// If-Match (RFC 9110 §13.1.1 is strong comparison), so a write that went
// over it was refused 412 for good, and the retry after it read 304 under
// the weak comparison If-None-Match uses, keeping the same dead validator
// in the state forever: every later sync said "the file on the server
// changed meanwhile" until another computer overwrote the file or the
// server version was cleared by hand (Synology DSM WebDAV, #7218). A weak
// ETag is kept out of the version whole — the write then goes
// unconditioned once, and the next read past the mtime's second brings a
// strong one back.
func TestSyncSurvivesAWeakETag(t *testing.T) {
	fake := &fakeDAV{files: map[string][]byte{}, etags: map[string]string{}, dirs: map[string]bool{"/dav": true},
		cond: true, putETag: true, weak: true}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ctx := context.Background()
	a := newComputer(t)
	a.use(t)
	if err := Configure(Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse", Keys: true, Agents: true}); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	// the weak ETag the first read saw is in the state now; a change here
	// writes over it
	if err := provider.Save(provider.Provider{ID: "one", Name: "One", Chat: "https://api.one.example/v1", Key: "k1"}); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil {
		t.Fatalf("the sync after the weak ETag: %v", err)
	}
	if err := provider.Save(provider.Provider{ID: "two", Name: "Two", Chat: "https://api.two.example/v1", Key: "k2"}); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil {
		t.Fatalf("the sync after it: %v", err)
	}
	if st := Status(); st.Error != "" {
		t.Fatalf("the sync stayed broken: %q", st.Error)
	}
	if n := fake.weakIfMatch + fake.weakIfNoneMatch; n > 0 {
		t.Fatalf("a weak ETag went out in a validator %d times", n)
	}
}

// versionOf keeps no weak ETag, whatever the server's other headers say:
// an ETag the write can't match is none at all.
func TestVersionOfKeepsNoWeakETag(t *testing.T) {
	h := http.Header{}
	h.Set("ETag", `W/"17-1767225600"`)
	if v := versionOf(h); v.ETag != "" {
		t.Fatalf("a weak ETag was kept: %q", v.ETag)
	}
	h.Set("Last-Modified", "Wed, 01 Jan 2025 00:00:02 GMT")
	h.Set("Date", "Wed, 01 Jan 2025 00:00:03 GMT")
	if v := versionOf(h); v.Modified == "" {
		t.Fatal("without the ETag, the Last-Modified was not kept")
	}
}
