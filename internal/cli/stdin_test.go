package cli

import (
	"os"
	"testing"
)

// The prompt reader must be built from the os.Stdin it is asked on, not
// from the one the process started with: the Windows desktop build starts
// with no console, and its init re-opens os.Stdin from the parent's
// console after package variables are set (#3737). The tests re-pipe
// stdin the same way, a line each.
func TestStdinReaderFollowsStdin(t *testing.T) {
	oldStdin := os.Stdin
	t.Cleanup(func() { os.Stdin = oldStdin })
	for _, want := range []string{"first\n", "second\n"} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		w.WriteString(want)
		w.Close()
		os.Stdin = r
		t.Cleanup(func() { r.Close() })
		line, err := stdinReader().ReadString('\n')
		if err != nil || line != want {
			t.Fatalf("asked on the fresh stdin, read %q: %v", line, err)
		}
	}
}
