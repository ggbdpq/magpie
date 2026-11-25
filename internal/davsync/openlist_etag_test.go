package davsync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/provider"
)

// openlistDAV answers as a self-hosted OpenList instance mounting a Synology
// DSM WebDAV storage in native proxy mode (#7651):
//   - GET (and HEAD) proxies the upstream response, returning upstream's ETag
//     (e.g. "c5bb8-661009fa5ece0") and honouring If-None-Match against it;
//   - PROPFIND (Depth 0) returns OpenList's own mtime-size ETag in <D:getetag>
//     (e.g. "6b053c0e-c5bb8");
//   - PUT checks If-Match strictly against OpenList's own mtime-size ETag and
//     answers 412 when it doesn't match, or 204 No Content (no ETag header,
//     unless putETag is set) when it does.
type openlistDAV struct {
	mu        sync.Mutex
	files     map[string][]byte
	getETags  map[string]string // upstream Synology ETag seen on GET
	propETags map[string]string // OpenList mtime-size ETag seen on PROPFIND & checked on PUT
	dirs      map[string]bool
	n         int
	putETag   bool

	// overrides for failed-read / XML edge-case tests
	propStatus int
	propBody   string

	// hooks fired once to inject concurrent writes at exact protocol points
	betweenPut          func()
	betweenPropAndGet   func()
	betweenVerifyAndPut func()
	verifySeen          bool

	// counters
	gets, fullGets, notModified int
	propfinds, puts, refused    int
	uncondOverwrites            int
}

func newOpenListDAV() *openlistDAV {
	return &openlistDAV{
		files:     map[string][]byte{},
		getETags:  map[string]string{},
		propETags: map[string]string{},
		dirs:      map[string]bool{"/dav": true},
	}
}

func (o *openlistDAV) write(p string, b []byte) {
	o.n++
	o.files[p] = append([]byte(nil), b...)
	o.getETags[p] = fmt.Sprintf(`"%x-661009fa5ece%x"`, len(b), o.n)
	o.propETags[p] = fmt.Sprintf(`"%x-%x"`, 0x6b053c0e+o.n*0x1380, len(b))
}

func (o *openlistDAV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if u, p, ok := r.BasicAuth(); !ok || u != "me" || p != "pw" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	p := r.URL.Path
	body, there := o.files[p]
	getTag := o.getETags[p]
	propTag := o.propETags[p]

	switch r.Method {
	case "PROPFIND":
		o.propfinds++
		if o.propStatus != 0 {
			w.WriteHeader(o.propStatus)
			if o.propBody != "" {
				io.WriteString(w, o.propBody)
			}
			return
		}
		if !there {
			if o.dirs[strings.TrimSuffix(p, "/")] {
				w.WriteHeader(http.StatusMultiStatus)
				return
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		if o.propBody != "" {
			io.WriteString(w, o.propBody)
		} else {
			fmt.Fprintf(w, `<?xml version="1.0" encoding="utf-8"?><D:multistatus xmlns:D="DAV:"><D:response><D:href>%s</D:href><D:propstat><D:prop><D:getetag>%s</D:getetag><D:getcontentlength>%d</D:getcontentlength></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response></D:multistatus>`, p, propTag, len(body))
		}
		if o.betweenPropAndGet != nil {
			fn := o.betweenPropAndGet
			o.betweenPropAndGet = nil
			fn()
		}
	case http.MethodGet, http.MethodHead:
		if r.Method == http.MethodGet {
			o.gets++
		}
		if !there {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodGet && r.Header.Get("If-None-Match") == getTag {
			o.notModified++
			w.Header().Set("ETag", getTag)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", getTag)
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			o.fullGets++
			w.Write(body)
			if o.verifySeen && o.betweenVerifyAndPut != nil {
				fn := o.betweenVerifyAndPut
				o.betweenVerifyAndPut = nil
				fn()
			}
		}
	case http.MethodPut:
		if !o.dirs[urlDir(p)] {
			w.WriteHeader(http.StatusConflict)
			return
		}
		im := r.Header.Get("If-Match")
		if there && im == "" {
			o.uncondOverwrites++
		}
		if im != "" {
			if o.betweenPut != nil {
				fn := o.betweenPut
				o.betweenPut = nil
				fn()
				propTag = o.propETags[p]
			}
			if im != propTag {
				o.refused++
				o.verifySeen = true
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
		}
		b, _ := io.ReadAll(r.Body)
		o.puts++
		o.write(p, b)
		if o.putETag {
			w.Header().Set("ETag", o.propETags[p])
		}
		if there {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusCreated)
	case "MKCOL":
		d := strings.TrimSuffix(p, "/")
		if o.dirs[d] {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		o.dirs[d] = true
		w.WriteHeader(http.StatusCreated)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// #7651: OpenList mounting a Synology WebDAV storage returns the upstream
// ETag on GET while checking its own PROPFIND getetag on PUT If-Match.
// Every sync after the first failed with "the file on the server changed
// meanwhile". Sync now verifies the file is unchanged and retries with
// PROPFIND's getetag, remembers that server uses PROPFIND ETags, and avoids
// both 412s and redundant downloads on later syncs.
func TestSyncOpenListETagMismatch(t *testing.T) {
	const name = "/dav/magpie/magpie.magpie-backup"
	for _, putETag := range []bool{false, true} {
		label := "no-put-etag"
		if putETag {
			label = "with-put-etag"
		}
		t.Run(label, func(t *testing.T) {
			fake := newOpenListDAV()
			fake.putETag = putETag
			srv := httptest.NewServer(fake)
			defer srv.Close()
			ctx := context.Background()
			cfg := Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse", Keys: true, Agents: true}

			a, b := newComputer(t), newComputer(t)
			a.use(t)
			provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k1"})
			if err := Configure(cfg); err != nil {
				t.Fatal(err)
			}
			// First sync creates the file (no If-Match).
			if err := Now(ctx); err != nil {
				t.Fatalf("first sync: %v", err)
			}
			// Second sync with a local edit hits the GET vs PROPFIND ETag
			// mismatch once, verifies the file is unchanged, and succeeds.
			provider.Save(provider.Provider{ID: "kimi", Name: "Kimi", Chat: "https://api.moonshot.cn/v1", Key: "k2"})
			if err := Now(ctx); err != nil {
				t.Fatalf("second sync (OpenList ETag mismatch): %v", err)
			}
			if fake.uncondOverwrites != 0 {
				t.Fatalf("unconditional overwrites: %d, want 0", fake.uncondOverwrites)
			}

			// After learning PropETag, later edits push with zero 412s.
			refusedBefore := fake.refused
			for _, id := range []string{"three", "four"} {
				provider.Save(provider.Provider{ID: id, Name: id, Chat: "https://" + id + "/v1", Key: "k" + id})
				if err := Now(ctx); err != nil {
					t.Fatalf("later sync (%s): %v", id, err)
				}
			}
			if fake.refused != refusedBefore {
				t.Fatalf("later syncs hit %d 412s after PropETag was learned, want 0", fake.refused-refusedBefore)
			}

			// Unchanged syncs check PROPFIND without downloading the file again.
			if err := Now(ctx); err != nil {
				t.Fatalf("settle sync: %v", err)
			}
			fullBefore, putsBefore := fake.fullGets, fake.puts
			for range 3 {
				if err := Now(ctx); err != nil {
					t.Fatalf("unchanged sync: %v", err)
				}
			}
			if fake.fullGets != fullBefore || fake.puts != putsBefore {
				t.Fatalf("unchanged syncs: %d full GETs, %d PUTs; want 0 of each", fake.fullGets-fullBefore, fake.puts-putsBefore)
			}

			// Second computer joins, adds a provider, and A receives it.
			b.use(t)
			if err := Configure(cfg); err != nil {
				t.Fatal(err)
			}
			if err := Now(ctx); err != nil {
				t.Fatalf("b join: %v", err)
			}
			provider.Save(provider.Provider{ID: "five", Name: "Five", Chat: "https://five/v1", Key: "k5"})
			if err := Now(ctx); err != nil {
				t.Fatalf("b push: %v", err)
			}
			a.use(t)
			if err := Now(ctx); err != nil {
				t.Fatalf("a pull b: %v", err)
			}
			if got := ids(); !slices.Equal(got, []string{"deepseek=k1", "five=k5", "four=kfour", "kimi=k2", "three=kthree"}) {
				t.Fatalf("a after b: %v", got)
			}
			remote, err := backup.Open(fake.files[name], cfg.Passphrase)
			if err != nil || len(remote.Providers) != 5 {
				t.Fatalf("remote backup: %v, providers=%d", err, len(remote.Providers))
			}
		})
	}
}

// Upgrading from an older sync-state.json that cached the upstream GET ETag:
// the first sync gets 304 Not Modified on GET and 412 on PUT. Clearing the
// cached tag on retry forces a full GET so OpenList recovery succeeds in one
// Now() call.
func TestSyncOpenListUpgradeFromCachedGETETag(t *testing.T) {
	fake := newOpenListDAV()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ctx := context.Background()
	cfg := Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse", Keys: true, Agents: true}

	newComputer(t).use(t)
	provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k1"})
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	// Unchanged sync caches the upstream GET ETag in sync-state.json without
	// having triggered a PUT 412 yet (matching a pre-fix state).
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	st := loadState()
	if st.Server.ETag == "" {
		t.Fatalf("pre-upgrade state: %+v, want cached GET ETag", st.Server)
	}

	// Now edit locally: the first attempt hits 304 -> 412, and the retry
	// clears the stale ETag, reads the file whole, and succeeds with PROPFIND.
	provider.Save(provider.Provider{ID: "kimi", Name: "Kimi", Chat: "https://api.moonshot.cn/v1", Key: "k2"})
	if err := Now(ctx); err != nil {
		t.Fatalf("sync after cached GET ETag: %v", err)
	}
	if fake.uncondOverwrites != 0 {
		t.Fatalf("unconditional overwrites: %d, want 0", fake.uncondOverwrites)
	}
	if fake.notModified == 0 {
		t.Fatalf("expected initial 304 Not Modified before retry, got 0")
	}
	// A follow-up edit must now push with zero additional 412s.
	refusedAfter := fake.refused
	provider.Save(provider.Provider{ID: "three", Name: "Three", Chat: "https://three/v1", Key: "k3"})
	if err := Now(ctx); err != nil {
		t.Fatalf("follow-up sync after upgrade recovery: %v", err)
	}
	if fake.refused != refusedAfter {
		t.Fatalf("follow-up sync hit %d 412s after learning PROPFIND ETag, want 0", fake.refused-refusedAfter)
	}
}

// Upload replacing a corrupt/non-backup file on OpenList also recovers from
// the GET vs PROPFIND ETag mismatch without dropping If-Match protection.
func TestUploadOpenListETagMismatch(t *testing.T) {
	const name = "/dav/magpie/magpie.magpie-backup"
	fake := newOpenListDAV()
	fake.dirs["/dav/magpie"] = true
	fake.write(name, []byte("not a backup"))
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ctx := context.Background()
	cfg := Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse", Keys: true}

	newComputer(t).use(t)
	provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k1"})
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err := Upload(ctx); err != nil {
		t.Fatalf("Upload on OpenList: %v", err)
	}
	if fake.uncondOverwrites != 0 {
		t.Fatalf("unconditional overwrites: %d, want 0", fake.uncondOverwrites)
	}
	if _, err := backup.Open(fake.files[name], cfg.Passphrase); err != nil {
		t.Fatalf("uploaded file is not a valid backup: %v", err)
	}
}

// Recovering from OpenList's ETag mismatch must never overwrite another
// computer's concurrent write:
//   - before PropETag is learned (B writes between A's GET and PUT)
//   - after PropETag is learned (B writes between A's PROPFIND+GET and PUT)
//   - during recovery (B writes between mismatchETag's verifying GET and retry PUT)
//   - during a PropETag read (B writes between PROPFIND and GET)
func TestSyncOpenListStillSeesConcurrentWrites(t *testing.T) {
	const name = "/dav/magpie/magpie.magpie-backup"
	ctx := context.Background()

	runConflict := func(t *testing.T, warmPropETag bool) {
		t.Helper()
		fake := newOpenListDAV()
		srv := httptest.NewServer(fake)
		defer srv.Close()
		cfg := Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse", Keys: true, Agents: true}

		a, b := newComputer(t), newComputer(t)
		a.use(t)
		provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k1"})
		if err := Configure(cfg); err != nil {
			t.Fatal(err)
		}
		if err := Now(ctx); err != nil {
			t.Fatal(err)
		}
		if warmPropETag {
			provider.Save(provider.Provider{ID: "warm", Name: "Warm", Chat: "https://warm/v1", Key: "kw"})
			if err := Now(ctx); err != nil {
				t.Fatal(err)
			}
			// Settle so A's cached state holds the PROPFIND version.
			if err := Now(ctx); err != nil {
				t.Fatal(err)
			}
		}

		b.use(t)
		if err := Configure(cfg); err != nil {
			t.Fatal(err)
		}
		if err := Now(ctx); err != nil {
			t.Fatal(err)
		}

		// Record server state before B writes, then let B push its own update,
		// then rewind the server to the snapshot and inject B's write right
		// before A's PUT is checked.
		fake.mu.Lock()
		before := append([]byte(nil), fake.files[name]...)
		beforeGet, beforeProp := fake.getETags[name], fake.propETags[name]
		fake.mu.Unlock()

		provider.Save(provider.Provider{ID: "kimi", Name: "Kimi", Chat: "https://api.moonshot.cn/v1", Key: "k2"})
		if err := Now(ctx); err != nil {
			t.Fatal(err)
		}
		fake.mu.Lock()
		bs := append([]byte(nil), fake.files[name]...)
		fake.files[name], fake.getETags[name], fake.propETags[name] = before, beforeGet, beforeProp
		fake.betweenPut = func() { fake.write(name, bs) }
		fake.mu.Unlock()

		a.use(t)
		provider.Save(provider.Provider{ID: "three", Name: "Three", Chat: "https://z/v1", Key: "k3"})
		if at := time.Now(); os.Chtimes(provider.Path(), at, at) != nil {
			t.Fatal("dating a's providers")
		}
		if err := Now(ctx); err != nil {
			t.Fatalf("a's sync with concurrent B write: %v", err)
		}
		// Both changed providers: A's newer providers win, and B's concurrent
		// write must have been seen and saved aside (never silently overwritten).
		n := Status().Notice
		if n == nil || !slices.Contains(n.There, "providers") || n.Saved == "" {
			t.Fatalf("a wrote over b's concurrent write without seeing it: notice=%+v", n)
		}
		saved, _ := filepath.Glob(filepath.Join(n.Saved, "*-server"+backup.Ext))
		if len(saved) != 1 {
			t.Fatalf("b's file kept aside: %v", saved)
		}
		kept, err := os.ReadFile(saved[0])
		if err != nil || string(kept) != string(bs) {
			t.Fatalf("kept copy does not match b's concurrent write: %v", err)
		}
	}

	t.Run("before_prop_detected", func(t *testing.T) { runConflict(t, false) })
	t.Run("after_prop_detected", func(t *testing.T) { runConflict(t, true) })

	t.Run("between_verify_get_and_retry_put", func(t *testing.T) {
		fake := newOpenListDAV()
		srv := httptest.NewServer(fake)
		defer srv.Close()
		d, err := newDAV(Config{URL: srv.URL + "/dav/", User: "me", Password: "pw"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.put(ctx, []byte("v1"), ""); err != nil {
			t.Fatal(err)
		}
		_, v, err := d.get(ctx, version{})
		if err != nil {
			t.Fatal(err)
		}
		// Another computer writes v2 AFTER mismatchETag's verifying GET and
		// BEFORE the retry PUT. The retry PUT carries PROPFIND's v1 tag, so
		// OpenList must reject it with 412 and keep v2 intact.
		fake.mu.Lock()
		fake.betweenVerifyAndPut = func() { fake.write(name, []byte("other computer v2")) }
		fake.mu.Unlock()
		if _, err := d.put(ctx, []byte("my write"), v.ETag); !errors.Is(err, errChanged) {
			t.Fatalf("put racing between verify GET and retry PUT: got %v, want errChanged", err)
		}
		fake.mu.Lock()
		got := string(fake.files[name])
		fake.mu.Unlock()
		if got != "other computer v2" {
			t.Fatalf("concurrent write was overwritten: got %q", got)
		}
	})

	t.Run("between_propfind_and_get", func(t *testing.T) {
		fake := newOpenListDAV()
		srv := httptest.NewServer(fake)
		defer srv.Close()
		d, err := newDAV(Config{URL: srv.URL + "/dav/", User: "me", Password: "pw"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.put(ctx, []byte("v0"), ""); err != nil {
			t.Fatal(err)
		}
		// Warm d so it learns that this server validates PUT If-Match against
		// PROPFIND's getetag.
		_, v0, err := d.get(ctx, version{})
		if err != nil {
			t.Fatal(err)
		}
		v1, err := d.put(ctx, []byte("v1"), v0.ETag)
		if err != nil {
			t.Fatalf("warm put: %v", err)
		}
		// Another computer writes v2 between PROPFIND and GET during the next
		// read. Because PROPFIND ran BEFORE GET, the returned ETag is v1's, so
		// PUT If-Match must be rejected with errChanged rather than overwriting.
		fake.mu.Lock()
		fake.betweenPropAndGet = func() { fake.write(name, []byte("other computer v2")) }
		fake.mu.Unlock()
		_, v, err := d.get(ctx, v1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.put(ctx, []byte("my write"), v.ETag); !errors.Is(err, errChanged) {
			t.Fatalf("put after write between PROPFIND and GET: got %v, want errChanged", err)
		}
		fake.mu.Lock()
		got := string(fake.files[name])
		fake.mu.Unlock()
		if got != "other computer v2" {
			t.Fatalf("concurrent write was overwritten: got %q", got)
		}
	})
}

// Failed PROPFIND reads (405, 500, malformed XML, empty getetag, or a getetag
// only in a 404 propstat) mean "unknown": put must return errChanged and must
// never fall back to an unconditional write. A multi-propstat response with a
// 200 OK getetag followed by a 404 Not Found propstat still resolves the 200
// OK getetag.
func TestDAVOpenListFailedPropfindDoesNotOverwrite(t *testing.T) {
	const name = "/dav/magpie/magpie.magpie-backup"
	ctx := context.Background()

	cases := []struct {
		name       string
		propStatus int
		propBody   string
	}{
		{name: "405", propStatus: http.StatusMethodNotAllowed},
		{name: "500", propStatus: http.StatusInternalServerError},
		{name: "malformed-xml", propStatus: http.StatusMultiStatus, propBody: `<D:multistatus><D:response`},
		{name: "empty-etag", propStatus: http.StatusMultiStatus, propBody: `<D:multistatus xmlns:D="DAV:"><D:response><D:propstat><D:prop><D:getetag>   </D:getetag></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response></D:multistatus>`},
		{name: "404-propstat", propStatus: http.StatusMultiStatus, propBody: `<D:multistatus xmlns:D="DAV:"><D:response><D:propstat><D:prop><D:getetag>"fake"</D:getetag></D:prop><D:status>HTTP/1.1 404 Not Found</D:status></D:propstat></D:response></D:multistatus>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newOpenListDAV()
			srv := httptest.NewServer(fake)
			defer srv.Close()
			d, err := newDAV(Config{URL: srv.URL + "/dav/", User: "me", Password: "pw"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := d.put(ctx, []byte("original"), ""); err != nil {
				t.Fatal(err)
			}
			_, v, err := d.get(ctx, version{})
			if err != nil {
				t.Fatal(err)
			}
			fake.mu.Lock()
			fake.propStatus = tc.propStatus
			fake.propBody = tc.propBody
			fake.mu.Unlock()

			if _, err := d.put(ctx, []byte("overwrite"), v.ETag); !errors.Is(err, errChanged) {
				t.Fatalf("failed PROPFIND (%s): got %v, want errChanged", tc.name, err)
			}
			fake.mu.Lock()
			got, uncond := string(fake.files[name]), fake.uncondOverwrites
			fake.mu.Unlock()
			if got != "original" || uncond != 0 {
				t.Fatalf("failed PROPFIND (%s) overwrote file: got %q, uncond=%d", tc.name, got, uncond)
			}
		})
	}

	t.Run("multi-propstat-200-then-404", func(t *testing.T) {
		fake := newOpenListDAV()
		srv := httptest.NewServer(fake)
		defer srv.Close()
		d, err := newDAV(Config{URL: srv.URL + "/dav/", User: "me", Password: "pw"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.put(ctx, []byte("original"), ""); err != nil {
			t.Fatal(err)
		}
		_, v, err := d.get(ctx, version{})
		if err != nil {
			t.Fatal(err)
		}
		fake.mu.Lock()
		propTag := fake.propETags[name]
		fake.propBody = fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?><D:multistatus xmlns:D="DAV:"><D:response><D:href>%s</D:href><D:propstat><D:prop><D:getetag>%s</D:getetag></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat><D:propstat><D:prop><D:getetag/></D:prop><D:status>HTTP/1.1 404 Not Found</D:status></D:propstat></D:response></D:multistatus>`, name, propTag)
		fake.mu.Unlock()

		v2, err := d.put(ctx, []byte("updated"), v.ETag)
		if err != nil {
			t.Fatalf("multi-propstat 200+404: %v", err)
		}
		fake.mu.Lock()
		got := string(fake.files[name])
		fake.propBody = "" // restore normal PROPFIND for follow-up check
		refusedBefore := fake.refused
		fake.mu.Unlock()
		if got != "updated" {
			t.Fatalf("got %q, want updated", got)
		}
		// Follow-up read+write with v2 should use PROPFIND directly (0 more 412s).
		_, v3, err := d.get(ctx, v2)
		if err != nil {
			t.Fatalf("follow-up get: %v", err)
		}
		if _, err := d.put(ctx, []byte("updated-2"), v3.ETag); err != nil {
			t.Fatalf("follow-up put: %v", err)
		}
		fake.mu.Lock()
		refusedAfter := fake.refused
		fake.mu.Unlock()
		if refusedAfter != refusedBefore {
			t.Fatalf("follow-up put hit %d 412s, want 0", refusedAfter-refusedBefore)
		}
	})
}
