package sessions

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

func TestCorruptFrameDoesNotExposeLineContinuation(t *testing.T) {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderCRC(true))
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	bad := enc.EncodeAll([]byte("lost prefix"), nil)
	bad[len(bad)-1] ^= 1
	data := append(bad, enc.EncodeAll([]byte("suffix\n"), nil)...)
	p := filepath.Join(t.TempDir(), "x.zstd")
	if err := os.WriteFile(p, data, 0600); err != nil {
		t.Fatal(err)
	}
	var lines []string
	next, _, end, err := scanFrames(p, 0, 0, func(b []byte) bool { lines = append(lines, string(b)); return true })
	if !errors.Is(err, zstd.ErrCRCMismatch) || len(lines) != 0 || next != 0 || end != 0 {
		t.Fatalf("lines=%q next=%d end=%d err=%v", lines, next, end, err)
	}
}

func TestDshCorruptSeedCacheRepair(t *testing.T) {
	setup(t)
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderCRC(true))
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	frame := func(s string) []byte { return enc.EncodeAll([]byte(s), nil) }
	head := frame("{\"type\":\"session\",\"id\":\"fork\",\"version\":3,\"isSeeded\":true}\n")
	inherited := frame("{\"type\":\"assistant/message\",\"seq\":1,\"time\":1790503290000,\"data\":{\"message\":{\"source\":{\"model\":\"m\"}},\"usage\":{\"inputTokens\":99,\"outputTokens\":1}}}\n")
	seed := frame("{\"type\":\"session/end-seed\",\"data\":{\"inherited\":true}}\n")
	bad := bytes.Clone(seed)
	bad[len(bad)-1] ^= 1
	own := frame("{\"type\":\"assistant/message\",\"seq\":2,\"time\":1790503291000,\"data\":{\"message\":{\"source\":{\"model\":\"m\"}},\"usage\":{\"inputTokens\":7,\"outputTokens\":1}}}\n")
	p := filepath.Join(t.TempDir(), "session.zstd")
	build := func(mid []byte) []byte {
		b := append(bytes.Clone(head), inherited...)
		b = append(b, mid...)
		return append(b, own...)
	}
	if err := os.WriteFile(p, build(bad), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	f := file{agent: "dsh", path: p, size: info.Size(), mod: modOf(info.ModTime()), main: true}
	mu.Lock()
	cache = make(map[string]*state)
	refresh([]file{f}, nil)
	summary := cache[p]
	mu.Unlock()
	if len(summary.Models) != 0 {
		t.Fatalf("corrupt fork counted inherited or later usage: %+v", summary.Models)
	}
	calls := readCalls(f)
	if len(calls.Calls) != 0 {
		t.Fatalf("corrupt fork counted calls: %+v", calls.Calls)
	}
	if err := os.WriteFile(p, build(seed), 0600); err != nil {
		t.Fatal(err)
	}
	fixedTime := info.ModTime().Add(time.Second)
	if err := os.Chtimes(p, fixedTime, fixedTime); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	f.size, f.mod = info.Size(), modOf(info.ModTime())
	mu.Lock()
	refresh([]file{f}, nil)
	summary = cache[p]
	mu.Unlock()
	m, ok := summary.Models.get("m")
	if !ok || m.Input != 7 {
		t.Fatalf("repaired summary: %+v", summary.Models)
	}
	calls = readCalls(f)
	if len(calls.Calls) != 1 || calls.Calls[0].Input != 7 {
		t.Fatalf("repaired calls: %+v", calls.Calls)
	}
}
