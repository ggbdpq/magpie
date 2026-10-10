package plugin

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The host flushes pi's auth.json before renaming it over the old, as it
// does plugin-auth.json's (#1505): a flush that fails leaves pi's sign-ins
// there as they were, rather than the write going in unflushed, where a
// machine that went down left the file all zero and the next mirror wrote
// the accounts it held over what was left.
func TestPiAuthSyncedBeforeRename(t *testing.T) {
	home, _ := os.UserHomeDir()
	sandbox(t)
	// pi is fetched once into Bun's own cache, not each run's HOME
	if os.Getenv("BUN_INSTALL_CACHE_DIR") == "" && home != "" {
		t.Setenv("BUN_INSTALL_CACHE_DIR", filepath.Join(home, ".bun", "install", "cache"))
	}
	// the plugin that makes the disk refuse a flush while the file named
	// by $FSYNC_FAIL is there; the flag file is made when the write under
	// test is the next one
	flag := filepath.Join(t.TempDir(), "fail")
	t.Setenv("FSYNC_FAIL", flag)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	if abs, err := filepath.Abs("testdata/nofsync"); err != nil {
		t.Fatal(err)
	} else if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs("testdata/pi")
	if _, err := Add(ctx, abs); err != nil {
		if strings.Contains(err.Error(), "bun add") {
			t.Skipf("pi couldn't be installed: %v", err)
		}
		t.Fatal(err)
	}
	piAuth := filepath.Join(filepath.Dir(AuthPath()), "pi", "auth.json")

	// a key account, written out to pi's auth.json by the mirror the
	// models hook runs when the providers are listed
	if _, err := APIKey(ctx, "fakepi-key", 0, nil, "k-old", NewAccount); err != nil {
		t.Fatal(err)
	}
	if _, err := Providers(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(piAuth)
	if err != nil {
		t.Fatalf("pi's auth.json wasn't written: %v", err)
	}
	if !strings.Contains(string(before), "k-old") {
		t.Fatalf("pi's auth.json holds no sign-in: %s", before)
	}

	// the account signed out, the mirror's next write takes it out of pi's
	// auth.json; the disk refuses the flush, and the sign-ins stay
	if err := SignOut(ctx, "fakepi-key", ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(flag, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Providers(ctx); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(piAuth)
	if !bytes.Equal(after, before) {
		t.Fatalf("a failed flush changed pi's auth.json:\n%s\nwas\n%s", after, before)
	}
	if left, _ := filepath.Glob(piAuth + ".tmp-*"); len(left) != 0 {
		t.Fatalf("a failed write left %v", left)
	}
}
