package davsync

// The request archive in the bucket is kept under its size (#5558: a
// bucket twenty days old held 3.6 GiB of 7,262 calls, none ever removed,
// while the archive on this computer has had a size and the oldest
// removed past it all along): past that size — settings'
// RequestArchiveLocalMB, the same one — the oldest calls are removed
// until a tenth under it, the call just kept never among them.

import (
	"bytes"
	"context"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

func newArchiveBucket(t *testing.T, f *fakeS3, srv *httptest.Server) *Bucket {
	t.Helper()
	s, err := newS3(Config{URL: "s3://bkt", Endpoint: srv.URL, User: f.id, Password: f.secret})
	if err != nil {
		t.Fatal(err)
	}
	return &Bucket{s}
}

// archiveCalls is the calls in the fake bucket, oldest first.
func archiveCalls(t *testing.T, f *fakeS3) []string {
	t.Helper()
	var ks []string
	for k := range f.objects {
		if strings.HasPrefix(k, "magpie/archive/") {
			ks = append(ks, k)
		}
	}
	slices.Sort(ks)
	return ks
}

func TestS3ArchiveKeptUnderItsSize(t *testing.T) {
	newComputer(t).use(t)
	f, srv := newFakeS3(t)
	b := newArchiveBucket(t, f, srv)
	settings.Save(settings.Settings{RequestArchiveLocalMB: 2}) // 2 MiB

	mib := bytes.Repeat([]byte("a"), 1<<20)
	put := func(id string) {
		t.Helper()
		if err := b.Put(context.Background(), "archive/2026-11-08/"+id+".json", mib); err != nil {
			t.Fatal(err)
		}
	}

	put("120000-aaa")
	put("120001-bbb")
	if ks := archiveCalls(t, f); len(ks) != 2 {
		t.Fatalf("two calls under the size, bucket keeps %v", ks)
	}
	put("120002-ccc") // three MiB under a two: the two oldest go
	ks := archiveCalls(t, f)
	if len(ks) != 1 || !strings.Contains(ks[0], "120002-ccc") {
		t.Fatalf("kept under 2 MiB, want only the newest call, got %v", ks)
	}
}

func TestS3ArchiveKeepsTheCallJustKept(t *testing.T) {
	newComputer(t).use(t)
	f, srv := newFakeS3(t)
	b := newArchiveBucket(t, f, srv)
	settings.Save(settings.Settings{RequestArchiveLocalMB: 1})

	big := bytes.Repeat([]byte("a"), 2<<20) // the call alone over the size
	if err := b.Put(context.Background(), "archive/2026-11-08/120000-aaa.json", big); err != nil {
		t.Fatal(err)
	}
	if ks := archiveCalls(t, f); len(ks) != 1 {
		t.Fatalf("the call just kept is never removed, bucket keeps %v", ks)
	}
}

func TestS3ArchiveTrimAcrossPages(t *testing.T) {
	newComputer(t).use(t)
	f, srv := newFakeS3(t)
	f.pageMax = 1 // one call a page: the trimming reads them all all the same
	b := newArchiveBucket(t, f, srv)
	settings.Save(settings.Settings{RequestArchiveLocalMB: 2})

	mib := bytes.Repeat([]byte("a"), 1<<20)
	for _, id := range []string{"120000-aaa", "120001-bbb", "120002-ccc", "120003-ddd", "120004-eee"} {
		if err := b.Put(context.Background(), "archive/2026-11-08/"+id+".json", mib); err != nil {
			t.Fatal(err)
		}
	}
	ks := archiveCalls(t, f)
	if len(ks) != 1 || !strings.Contains(ks[0], "120004-eee") {
		t.Fatalf("kept under 2 MiB across pages, want only the newest call, got %v", ks)
	}
}

func TestS3ArchiveTrimFailureKeepsThePut(t *testing.T) {
	newComputer(t).use(t)
	f, srv := newFakeS3(t)
	b := newArchiveBucket(t, f, srv)
	settings.Save(settings.Settings{RequestArchiveLocalMB: 2})

	mib := bytes.Repeat([]byte("a"), 1<<20)
	put := func() {
		t.Helper()
		if err := b.Put(context.Background(), "archive/2026-11-08/12000"+strconv.Itoa(len(archiveCalls(t, f)))+"-x.json", mib); err != nil {
			t.Fatal(err)
		}
	}
	f.failList = true
	put()
	put()
	put() // three MiB past two, but the listing refuses: nothing removed
	if ks := archiveCalls(t, f); len(ks) != 3 {
		t.Fatalf("a failed trimming keeps every call, bucket keeps %v", ks)
	}
	f.failList = false
	put() // the next write tries again: now the oldest go
	ks := archiveCalls(t, f)
	if len(ks) != 1 || !strings.HasSuffix(ks[0], "120003-x.json") {
		t.Fatalf("the trimming comes back with the next call, want only the newest, got %v", ks)
	}
}
