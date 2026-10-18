package sessions

import (
	"bytes"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/steady"
)

// #2814: a call-cache shard is written through a temp file flushed to the
// disk before it is renamed in, as every other file magpie keeps is
// (#1505), and a rename that fails is said once per shard rather than
// thrown away. The append path is left as it is: its frames carry their own
// CRC and a torn tail is caught by the reader.
func TestCallShardIsFlushedBeforeItsRenameAndAFailedRenameIsSaid(t *testing.T) {
	d := setupCalls(t)
	path := filepath.Join(d.claude, "projects", "p", "s.jsonl")
	writeLines(t, path, claudeMsg("m1", "claude-sonnet-5", 10, 1, 2, 3, 1))
	defer func() { steady.Sync = (*os.File).Sync }()
	shard := callCachePath(path)

	// A flush that fails leaves no shard: one renamed in before its data is
	// on the disk can come back all zero or cut short after a forced
	// restart, and every such shard is a full re-parse of the session file.
	steady.Sync = func(*os.File) error { return errors.New("the disk went away") }
	if calls := Calls(time.Time{}); len(calls) != 1 {
		t.Fatalf("calls read without a shard: %+v", calls)
	}
	if _, err := os.Stat(shard); !os.IsNotExist(err) {
		t.Fatalf("a shard whose flush failed was renamed in (%v)", err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(shard), "."+filepath.Base(shard)+".*")); len(left) != 0 {
		t.Fatalf("temp files left by a flush that failed: %v", left)
	}

	// A flush that works: the temp file beside the shard is flushed while
	// the shard is not there yet, and the shard reads back afterwards.
	flushed := false
	steady.Sync = func(f *os.File) error {
		if filepath.Dir(f.Name()) != filepath.Dir(shard) || filepath.Base(f.Name()) == filepath.Base(shard) {
			t.Errorf("flushed %s, not a temp file beside %s", f.Name(), shard)
		}
		if _, err := os.Stat(shard); err == nil {
			t.Errorf("%s was renamed in before its data was flushed", shard)
		}
		flushed = true
		return f.Sync()
	}
	resetCalls()
	if calls := Calls(time.Time{}); len(calls) != 1 {
		t.Fatalf("calls: %+v", calls)
	}
	if !flushed {
		t.Fatal("the shard was written without being flushed to the disk")
	}
	if st := loadCalls(file{path: path, agent: "claude"}); st == nil || len(st.Calls) != 1 {
		t.Fatalf("the flushed shard does not read back: %+v", st)
	}

	// A temp a steady.WriteFile left behind (the process went down between
	// its write and its rename) is removed an hour on, as calls-*.tmp was.
	abandoned := filepath.Join(callCacheDir(), "."+filepath.Base(shard)+".123456")
	if err := os.WriteFile(abandoned, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(abandoned, old, old); err != nil {
		t.Fatal(err)
	}
	callSwept = time.Time{} // the sweep runs once a minute now; sweep this read
	Calls(time.Time{})
	if _, err := os.Stat(abandoned); !os.IsNotExist(err) {
		t.Fatalf("an abandoned temp remains: %v", err)
	}

	// The append path is unchanged: a frame carries its own CRC, so an
	// append grows the shard in place and never goes through the flush.
	before, err := os.ReadFile(shard)
	if err != nil {
		t.Fatal(err)
	}
	steady.Sync = func(*os.File) error {
		t.Error("an append flushed the shard as if it were a new one")
		return nil
	}
	appendText(t, path, claudeMsg("m2", "claude-sonnet-5", 20, 2, 0, 0, 2)+"\n")
	if calls := Calls(time.Time{}); len(calls) != 2 {
		t.Fatalf("calls after an append: %+v", calls)
	}
	after, err := os.ReadFile(shard)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(after, before) || len(after) <= len(before) {
		t.Fatalf("an append rewrote the shard: %d -> %d bytes", len(before), len(after))
	}

	// A rename that fails (on Windows a reader or a virus scanner holding
	// the shard open; here a folder in its place) is said once, not thrown
	// away: every read until one lands re-parses the whole session file.
	if err := os.Remove(shard); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(shard, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shard, "held"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	was := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(was)
	steady.Sync = (*os.File).Sync
	resetCalls()
	if calls := Calls(time.Time{}); len(calls) != 2 {
		t.Fatalf("calls while the shard cannot be replaced: %+v", calls)
	}
	if !strings.Contains(logs.String(), shard) {
		t.Fatalf("a failed rename was not said: %q", logs.String())
	}
	said := logs.Len()
	resetCalls()
	if calls := Calls(time.Time{}); len(calls) != 2 {
		t.Fatalf("calls while the shard cannot be replaced: %+v", calls)
	}
	if logs.Len() != said {
		t.Fatalf("the same failed rename was said again: %q", logs.String()[said:])
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(shard), "."+filepath.Base(shard)+".*")); len(left) != 0 {
		t.Fatalf("temp files left after a failed rename: %v", left)
	}
}
