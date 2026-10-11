package backup

import (
	"errors"
	"testing"
)

// A backup file an editor saved over with a byte order mark (a Windows
// editor's "UTF-8 with BOM", PowerShell 5.1's Set-Content) still opens:
// the mark sits in front of the sealed JSON, and the write through
// edit.WriteAtomic keeps the file's mark (#1534), so the reader trims it.
// A file that isn't a backup at all stays that, mark or not, and one cut
// short is still a damaged backup a sync may rebuild.
func TestOpenReadsThroughABOM(t *testing.T) {
	home(t)
	setUp(t)
	b, err := Collect(true, "test")
	if err != nil {
		t.Fatal(err)
	}
	data, err := Seal(b, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	mark := []byte("\xef\xbb\xbf")
	got, err := Open(append(append([]byte(nil), mark...), data...), "correct horse")
	if err != nil {
		t.Fatalf("a backup with a byte order mark: %v", err)
	}
	if len(got.Providers) != len(b.Providers) {
		t.Fatalf("providers: %d, want %d", len(got.Providers), len(b.Providers))
	}
	if _, err := Open(append(append([]byte(nil), mark...), []byte("<html>sign in</html>")...), "correct horse"); !errors.Is(err, ErrNotBackup) {
		t.Fatalf("a marked page that is no backup: %v", err)
	}
	if _, err := Open(append(append([]byte(nil), mark...), data[:len(data)/2]...), "correct horse"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("a marked backup cut short: %v", err)
	}
}
