package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// memoKeptPath is the file a sync keeps what it found in, for the next
// one to start from.
func memoKeptPath() string { return filepath.Join(Dir(), "treememo.json") }

// restartMemo is a process restart for what is kept in memory: the next
// sync starts from what the last one kept in its file alone.
func restartMemo(t *testing.T) {
	t.Helper()
	memo.Lock()
	memo.hashes = map[string]hashMemo{}
	memo.trees = map[[2]string]treeMemo{}
	memo.Unlock()
}

// A sync that found a library unchanged keeps what that took, for the
// next sync to start from: what a walk still agrees with isn't read
// again, run after run (#7740). What a run couldn't make sense of keeps
// nothing: the next starts over, never taking it as unchanged.
func TestSyncKeepsWhatItFoundAcrossRestarts(t *testing.T) {
	h := sandbox(t)
	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	write(t, filepath.Join(src, "pdf/scripts/run.sh"), "echo hi\n")
	ok(t)(SetSkillHow("", HowCopy))
	ok(t)(InstallSkills(src, []string{"pdf"}, []string{"claude", "codex"}))
	age(t, h)
	ok(t)(Sync())
	kept, err := os.ReadFile(memoKeptPath())
	if err != nil || !json.Valid(kept) {
		t.Fatalf("the sync kept nothing the next one could start from: %v", err)
	}

	// a restart, and a sync of the unchanged library: the copies are as
	// the library's, and are not made again
	restartMemo(t)
	made, _ := copiedAt(filepath.Join(h, ".claude/skills/pdf"))
	ok(t)(Sync())
	if now, _ := copiedAt(filepath.Join(h, ".claude/skills/pdf")); !now.Equal(made) {
		t.Fatal("the next sync made a copy again that the last run had found fresh")
	}

	// the library's changes while no run was looking: the kept answers
	// say nothing of it, and the copies are made again
	newer := time.Now().Add(time.Minute)
	lib := filepath.Join(skillDir("pdf"), "scripts/run.sh")
	write(t, lib, "echo new\n")
	if err := os.Chtimes(lib, newer, newer); err != nil {
		t.Fatal(err)
	}
	ok(t)(Sync())
	for _, a := range []string{"claude", "codex"} {
		if got := read(t, filepath.Join(h, "."+a, "skills/pdf/scripts/run.sh")); got != "echo new\n" {
			t.Fatalf("%s's copy wasn't made again after the library's changed: %q", a, got)
		}
	}

	// a file that says nothing this shape can read keeps nothing: the
	// next sync starts over, and keeps what it found
	age(t, h) // the copies and the library settled, so what it finds is kept
	write(t, memoKeptPath(), "not what a sync kept")
	restartMemo(t)
	ok(t)(Sync())
	kept, err = os.ReadFile(memoKeptPath())
	if err != nil || !json.Valid(kept) {
		t.Fatalf("the sync after a file it couldn't read kept nothing: %v", err)
	}
}

// An unchanged library is not read again by the sync after the one that
// found it unchanged: a new process starts from what the last kept, and
// opens none of the skills' files (0.1.4762 read 3.6 GB a run, #7740).
// Measured as Windows measures what this process reads, which is where
// the report did.
func TestRestartedSyncDoesntRereadUnchangedSkills(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("measures what the process reads, as Windows counts it")
	}
	h := sandbox(t)
	src := filepath.Join(h, "src/skills")
	for _, n := range []string{"pdf", "xlsx"} {
		skill(t, filepath.Join(src, n), n, n)
		write(t, filepath.Join(src, n, "data/blob.bin"), strings.Repeat("x", 3<<20))
	}
	ok(t)(SetSkillHow("", HowCopy))
	ok(t)(InstallSkills(src, []string{"pdf", "xlsx"}, []string{"claude", "codex"}))
	age(t, h)
	ok(t)(Sync()) // the run before: what it found is kept for the next
	restartMemo(t)
	before := processReadBytes(t)
	ok(t)(Sync())
	got := processReadBytes(t) - before
	t.Logf("the sync after a restart read %d bytes", got)
	if got >= 4<<20 {
		t.Fatalf("a sync of an unchanged library read %d bytes of the skills again", got)
	}
}
