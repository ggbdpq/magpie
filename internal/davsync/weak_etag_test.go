package davsync

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
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
// unconditioned once (marked Blind), and the next read past the mtime's
// second brings a strong one back.
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

// versionOf and strongOf keep no weak ETag (including padded whitespace),
// and version.conditions() never sends a weak validator persisted by an
// older sync-state.json.
func TestVersionOfKeepsNoWeakETag(t *testing.T) {
	h := http.Header{}
	h.Set("ETag", ` W/"17-1767225600" `)
	if v := versionOf(h); v.ETag != "" {
		t.Fatalf("a weak ETag was kept: %q", v.ETag)
	}
	if c := (version{ETag: ` W/"17-1767225600" `}).conditions(); c != nil {
		t.Fatalf("conditions sent a weak ETag: %v", c)
	}
	h.Set("Last-Modified", "Wed, 01 Jan 2025 00:00:02 GMT")
	h.Set("Date", "Wed, 01 Jan 2025 00:00:03 GMT")
	if v := versionOf(h); v.Modified == "" {
		t.Fatal("without the ETag, the Last-Modified was not kept")
	}
	if c := (version{ETag: `W/"17-1767225600"`, Modified: "Wed, 01 Jan 2025 00:00:02 GMT"}).conditions(); c["If-Modified-Since"] == "" || c["If-None-Match"] != "" {
		t.Fatalf("conditions with weak ETag + Last-Modified: %v", c)
	}
}

// Upgrading from a stuck sync-state.json that holds a weak ETag self-heals
// to the server's strong ETag on the very first read-only sync, rather than
// continuing to receive 304 on If-None-Match: W/"…" until the next write.
func TestSyncHealsStuckWeakETagOnReadOnlySync(t *testing.T) {
	const filePath = "/dav/magpie/magpie.magpie-backup"
	fake := &fakeDAV{files: map[string][]byte{}, etags: map[string]string{}, dirs: map[string]bool{"/dav": true},
		cond: true, putETag: true}
	var sentWeakINM int
	// Server serves strong ETags (past the mtime second) but compares
	// If-None-Match weakly per RFC 9110 §13.1.2, so a client sending
	// If-None-Match: W/"1" gets 304 instead of healing to "1".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == filePath {
			if inm := strings.TrimSpace(r.Header.Get("If-None-Match")); strings.HasPrefix(inm, "W/") {
				fake.mu.Lock()
				cur := fake.etags[filePath]
				fake.mu.Unlock()
				if strings.TrimPrefix(inm, "W/") == cur {
					sentWeakINM++
					w.Header().Set("ETag", cur)
					w.WriteHeader(http.StatusNotModified)
					return
				}
			}
		}
		fake.ServeHTTP(w, r)
	}))
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
	// Simulate an existing stuck sync-state.json written before the fix
	st := loadState()
	wantStrong := st.Server.ETag
	if wantStrong == "" || strings.HasPrefix(wantStrong, "W/") {
		t.Fatalf("initial strong ETag missing: %q", wantStrong)
	}
	st.Server.ETag = "W/" + wantStrong
	saveState(st)

	if err := Now(ctx); err != nil {
		t.Fatalf("read-only heal sync: %v", err)
	}
	if got := loadState().Server.ETag; got != wantStrong {
		t.Fatalf("state ETag after read-only sync = %q, want healed strong %q", got, wantStrong)
	}
	if sentWeakINM != 0 {
		t.Fatalf("sent weak If-None-Match %d times", sentWeakINM)
	}
}

// Once PUT gets 412 (errChanged), syncNow clears cached read validators
// (preserving NoCondRead) before retrying syncOnce so any If-None-Match
// (304) vs If-Match (412) mismatch — such as a legacy "-gzip" ETag left in
// sync-state.json — recovers in a single sync instead of looping on 304/412.
func TestSyncClearsValidatorOnErrChangedRetry(t *testing.T) {
	const filePath = "/dav/magpie/magpie.magpie-backup"
	fake := &fakeDAV{files: map[string][]byte{}, etags: map[string]string{}, dirs: map[string]bool{"/dav": true},
		cond: true, putETag: true}
	// Wrap fakeDAV so If-None-Match with a legacy "-gzip" ETag returns 304
	// while If-Match rejects it with 412 (as Apache mod_deflate did).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == filePath {
			if inm := r.Header.Get("If-None-Match"); strings.HasSuffix(inm, `-gzip"`) {
				fake.mu.Lock()
				cur := fake.etags[filePath]
				fake.mu.Unlock()
				if strings.TrimSuffix(inm, `-gzip"`)+`"` == cur {
					w.Header().Set("ETag", cur)
					w.WriteHeader(http.StatusNotModified)
					return
				}
			}
		}
		fake.ServeHTTP(w, r)
	}))
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
	st := loadState()
	st.Server.ETag = strings.TrimSuffix(st.Server.ETag, `"`) + `-gzip"`
	saveState(st)

	if err := provider.Save(provider.Provider{ID: "one", Name: "One", Chat: "https://api.one.example/v1", Key: "k1"}); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil {
		t.Fatalf("sync with stale -gzip ETag should recover on errChanged retry: %v", err)
	}
	if got := loadState().Server.ETag; strings.HasSuffix(got, `-gzip"`) || got == "" {
		t.Fatalf("state ETag after recovery = %q, want fresh strong ETag", got)
	}

	// Also verify NoCondRead is preserved when errChanged clears validators.
	var sawCondGetAfterRetry bool
	var force412 bool
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == filePath && r.Header.Get("If-None-Match") != "" {
			sawCondGetAfterRetry = true
		}
		if r.Method == http.MethodPut && r.URL.Path == filePath && force412 {
			force412 = false
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		fake.ServeHTTP(w, r)
	}))
	defer srv2.Close()
	if err := Configure(Config{URL: srv2.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse", Keys: true, Agents: true}); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	st = loadState()
	st.Server.NoCondRead = true
	saveState(st)
	if err := provider.Save(provider.Provider{ID: "two", Name: "Two", Chat: "https://api.two.example/v1", Key: "k2"}); err != nil {
		t.Fatal(err)
	}
	force412 = true
	if err := Now(ctx); err != nil {
		t.Fatalf("sync with NoCondRead=true after 412 retry: %v", err)
	}
	if force412 {
		t.Fatal("expected injected 412 to trigger errChanged retry")
	}
	if !loadState().Server.NoCondRead {
		t.Fatal("NoCondRead was lost across errChanged retry")
	}
	if sawCondGetAfterRetry {
		t.Fatal("sent conditional GET despite NoCondRead=true")
	}
}

// S3: a gateway/proxy that returns weak ETags must not make s3.put send
// If-None-Match: * against an already-existing object (deterministic 412
// loop), either on the conditional path or the unconditional HEAD fallback,
// and must still detect concurrent writes and strip weak ETags on return.
func TestS3SyncSurvivesWeakETag(t *testing.T) {
	for _, tc := range []struct {
		name   string
		noCond bool
	}{
		{"conditional", false},
		{"unconditional_head_fallback", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, srv := newFakeS3(t)
			f.weak = true
			f.noCond = tc.noCond
			cfg := f.config(srv)
			ctx := context.Background()
			a := newComputer(t)
			a.use(t)
			if err := Configure(cfg); err != nil {
				t.Fatal(err)
			}
			// Sync 1: creates object on empty bucket (If-None-Match: *)
			if err := Now(ctx); err != nil {
				t.Fatalf("initial sync: %v", err)
			}
			if got := loadState().Server.ETag; got != "" {
				t.Fatalf("weak ETag leaked into state after initial PUT: %q", got)
			}

			// Sync 2: modifies locally and overwrites existing weak-ETag object
			if err := provider.Save(provider.Provider{ID: "one", Name: "One", Chat: "https://api.one.example/v1", Key: "k1"}); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			beforeLog := len(f.log)
			f.mu.Unlock()
			if err := Now(ctx); err != nil {
				t.Fatalf("overwrite sync over weak ETag: %v (log: %q)", err, f.log[beforeLog:])
			}
			if st := loadState(); st.Server.ETag != "" {
				t.Fatalf("weak ETag leaked into state after overwrite PUT: %q", st.Server.ETag)
			} else if st.Blind == nil {
				t.Fatal("expected Blind state after unconditional overwrite over weak ETag")
			}
			f.mu.Lock()
			ops := append([]string(nil), f.log[beforeLog:]...)
			f.mu.Unlock()
			for _, op := range ops {
				if op == "PUT none:*" || strings.Contains(op, "W/") {
					t.Fatalf("forbidden validator sent on existing weak-ETag object: %q (all: %q)", op, ops)
				}
			}

			// Direct s.put with padded weak ETag detects concurrent change via HEAD
			s, err := newS3(cfg)
			if err != nil {
				t.Fatal(err)
			}
			const key = "team x+y/magpie/magpie.magpie-backup"
			f.mu.Lock()
			curWeak := " W/" + f.etags[key] + " "
			f.mu.Unlock()
			if v, err := s.put(ctx, []byte("direct-over-weak"), curWeak); err != nil {
				t.Fatalf("direct s.put over matching weak ETag: %v", err)
			} else if !v.Blind || v.ETag != "" {
				t.Fatalf("direct s.put return = %+v, want Blind=true and empty ETag", v)
			}
			// Stale weak ETag must still return errChanged
			if _, err := s.put(ctx, []byte("stale-over-weak"), curWeak); !errors.Is(err, errChanged) {
				t.Fatalf("direct s.put over stale weak ETag: %v, want errChanged", err)
			}
		})
	}
}

// When dav.put falls back to an unconditional PUT over an existing file
// because its ETag was weak, marking the write Blind preserves concurrent
// write detection so two devices syncing back-to-back merge instead of
// losing one device's edits.
func TestDAVWeakETagBlindWriteMergesConcurrentChange(t *testing.T) {
	fake := &fakeDAV{files: map[string][]byte{}, etags: map[string]string{}, dirs: map[string]bool{"/dav": true},
		cond: true, putETag: true, weak: true}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	cfg := Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse", Keys: true, Agents: true}
	ctx := context.Background()
	const filePath = "/dav/magpie/magpie.magpie-backup"

	a, b := newComputer(t), newComputer(t)
	a.use(t)
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k1"}); err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(settings.Settings{Theme: "light"}); err != nil {
		t.Fatal(err)
	}
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}

	b.use(t)
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}

	// Capture the common file both read before A's blind write
	fake.mu.Lock()
	common := append([]byte(nil), fake.files[filePath]...)
	commonETag := fake.etags[filePath]
	fake.mu.Unlock()

	// A edits provider and writes unconditionally (weak ETag -> Blind: true)
	a.use(t)
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek A", Chat: "https://api.deepseek.com/v1", Key: "k1"}); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	if loadState().Blind == nil {
		t.Fatal("expected Blind state after WebDAV weak-ETag unconditional PUT")
	}

	// Simulate B having read the common file before A's PUT landed, then
	// overwriting A's blind PUT with B's theme edit
	fake.mu.Lock()
	fake.files[filePath], fake.etags[filePath] = common, commonETag
	fake.mu.Unlock()
	b.use(t)
	s := settings.Load()
	s.Theme = "dark"
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}

	// A's next sync detects its blind write was overwritten and merges A's rename back up
	a.use(t)
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	raw := fake.files[filePath]
	fake.mu.Unlock()
	bundle, err := backup.Open(raw, cfg.Passphrase)
	if err != nil {
		t.Fatal(err)
	}
	var gotName string
	for _, p := range bundle.Providers {
		if p.ID == "deepseek" {
			gotName = p.Name
		}
	}
	if gotName != "DeepSeek A" || bundle.Settings.Theme != "dark" {
		t.Fatalf("merged server bundle = name:%q theme:%q, want DeepSeek A + dark", gotName, bundle.Settings.Theme)
	}
}
