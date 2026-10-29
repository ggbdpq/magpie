package sessions

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestScanFramesCorruption(t *testing.T) {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderCRC(true))
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	frame := func(s string) []byte { return enc.EncodeAll([]byte(s), nil) }
	first, last := frame("first\n"), frame("last\n")
	bad := frame("broken\n")
	bad[len(bad)-1] ^= 0xff
	malformed := []byte{0, 0, 0, 0}

	for _, tc := range []struct {
		name      string
		middle    []byte
		want      []string
		badHeader bool
	}{
		{"complete corrupt frame", bad, []string{"first", "last"}, false},
		{"malformed header", malformed, []string{"first"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "session.zstd")
			data := append(append(bytes.Clone(first), tc.middle...), last...)
			if err := os.WriteFile(p, data, 0o600); err != nil {
				t.Fatal(err)
			}
			var lines []string
			next, skip, end, err := scanFrames(p, 0, 0, func(b []byte) bool { lines = append(lines, string(b)); return true })
			if !reflect.DeepEqual(lines, tc.want) {
				t.Fatalf("lines = %q, want %q", lines, tc.want)
			}
			if tc.badHeader {
				if !errors.Is(err, errFrame) || next != int64(len(first)) || end != next {
					t.Fatalf("malformed header: next=%d skip=%d end=%d err=%v", next, skip, end, err)
				}
			} else if err != nil || next != int64(len(data)) || end != next || skip != 0 {
				t.Fatalf("corrupt frame: next=%d skip=%d end=%d err=%v", next, skip, end, err)
			}
		})
	}
}

func TestScanFramesIncompleteRetained(t *testing.T) {
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	first, last := enc.EncodeAll([]byte("first\n"), nil), enc.EncodeAll([]byte("last\n"), nil)
	p := filepath.Join(t.TempDir(), "session.zstd")
	cut := len(last) / 2
	if err := os.WriteFile(p, append(bytes.Clone(first), last[:cut]...), 0o600); err != nil {
		t.Fatal(err)
	}
	var lines []string
	next, skip, end, err := scanFrames(p, 0, 0, func(b []byte) bool { lines = append(lines, string(b)); return true })
	if err != nil || next != int64(len(first)) || end != next || skip != 0 || !reflect.DeepEqual(lines, []string{"first"}) {
		t.Fatalf("partial: lines=%q next=%d skip=%d end=%d err=%v", lines, next, skip, end, err)
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(last[cut:]); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, _, err = scanFrames(p, next, skip, func(b []byte) bool { lines = append(lines, string(b)); return true })
	if err != nil || !reflect.DeepEqual(lines, []string{"first", "last"}) {
		t.Fatalf("completed: lines=%q err=%v", lines, err)
	}
}

func TestDshReadersRecoverAfterCorruptFrame(t *testing.T) {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderCRC(true))
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	head := enc.EncodeAll([]byte("{\"type\":\"session\",\"id\":\"s\",\"version\":3}\n"), nil)
	bad := enc.EncodeAll([]byte("broken\n"), nil)
	bad[len(bad)-1] ^= 0xff
	reply := enc.EncodeAll([]byte("{\"type\":\"assistant/message\",\"seq\":1,\"time\":1790503290000,\"data\":{\"message\":{\"source\":{\"model\":\"m\"}},\"usage\":{\"inputTokens\":7,\"outputTokens\":1}}}\n"), nil)
	p := filepath.Join(t.TempDir(), "session.zstd")
	data := append(append(bytes.Clone(head), bad...), reply...)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	f := file{path: p, size: info.Size(), mod: info.ModTime(), main: true}
	s := parseDsh(f, nil)
	if s.ID != "s" || s.Dsh == nil || s.Dsh.Tail == nil || s.Dsh.Tail.Off != int64(len(data)) {
		t.Fatalf("summary: %+v", s)
	}
	calls, _, ok := readDshCalls(f, nil)
	if !ok || len(calls.Calls) != 1 || calls.Calls[0].Input != 7 || calls.Dsh.Tail == nil || calls.Dsh.Tail.Off != int64(len(data)) {
		t.Fatalf("calls: %+v, ok=%v", calls, ok)
	}
	if again, kept, ok := readDshCalls(f, calls); !ok || kept != 1 || len(again.Calls) != 1 {
		t.Fatalf("repeated read: %+v, kept=%d ok=%v", again, kept, ok)
	}
}

func TestScanFramesUnknownDictionaryRetained(t *testing.T) {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderDictRaw(42, []byte("dictionary content")))
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	frame := enc.EncodeAll([]byte("dictionary data\n"), nil)
	p := filepath.Join(t.TempDir(), "session.zstd")
	if err := os.WriteFile(p, frame, 0o600); err != nil {
		t.Fatal(err)
	}
	next, skip, end, err := scanFrames(p, 0, 0, func([]byte) bool { t.Fatal("decoded without dictionary"); return false })
	if !errors.Is(err, zstd.ErrUnknownDictionary) || next != 0 || skip != 0 || end != 0 {
		t.Fatalf("dictionary frame: next=%d skip=%d end=%d err=%v", next, skip, end, err)
	}
}

func TestScanFramesIncompleteContainsFrame(t *testing.T) {
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	inner := enc.EncodeAll([]byte("inside\n"), nil)
	// A raw block contains a complete frame as ordinary uncompressed data.
	size := len(inner) + 10
	outer := []byte{0x28, 0xb5, 0x2f, 0xfd, 0x20, byte(size)}
	block := size<<3 | 1
	outer = append(outer, byte(block), byte(block>>8), byte(block>>16))
	outer = append(outer, inner...)
	p := filepath.Join(t.TempDir(), "session.zstd")
	if err := os.WriteFile(p, outer, 0o600); err != nil {
		t.Fatal(err)
	}
	next, skip, end, err := scanFrames(p, 0, 0, func([]byte) bool { t.Fatal("read nested frame"); return false })
	if err != nil || next != 0 || skip != 0 || end != 0 {
		t.Fatalf("incomplete outer frame: next=%d skip=%d end=%d err=%v", next, skip, end, err)
	}
}

func TestScanFramesPartialMagic(t *testing.T) {
	p := filepath.Join(t.TempDir(), "session.zstd")
	magic := []byte{0x28, 0xb5, 0x2f, 0xfd}
	for n := 1; n < len(magic); n++ {
		if err := os.WriteFile(p, magic[:n], 0o600); err != nil {
			t.Fatal(err)
		}
		next, skip, end, err := scanFrames(p, 0, 0, func([]byte) bool { t.Fatal("partial header decoded"); return false })
		if err != nil || next != 0 || skip != 0 || end != 0 {
			t.Fatalf("%d-byte header: next=%d skip=%d end=%d err=%v", n, next, skip, end, err)
		}
	}
}

func TestScanFramesCorruptFrameInsideLine(t *testing.T) {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderCRC(true))
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	first := enc.EncodeAll([]byte("partial"), nil)
	bad := enc.EncodeAll([]byte("missing"), nil)
	bad[len(bad)-1] ^= 0xff
	last := enc.EncodeAll([]byte("end\n"), nil)
	p := filepath.Join(t.TempDir(), "session.zstd")
	if err := os.WriteFile(p, append(append(bytes.Clone(first), bad...), last...), 0o600); err != nil {
		t.Fatal(err)
	}
	var lines []string
	next, _, end, err := scanFrames(p, 0, 0, func(b []byte) bool { lines = append(lines, string(b)); return true })
	if err == nil || len(lines) != 0 || next != 0 || end != int64(len(first)) {
		t.Fatalf("partial line across corruption: lines=%q next=%d end=%d err=%v", lines, next, end, err)
	}
}
