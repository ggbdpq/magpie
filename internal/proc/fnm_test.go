package proc

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// fnmBins takes the platform's shape: the bin folder of each installation,
// or on Windows the installation itself, with the default alias (a link to
// the installation) hoisted ahead of the versions by number.
func TestFnmBinsShapes(t *testing.T) {
	for _, c := range []struct {
		name                 string
		versionBin, aliasBin string
	}{
		{"the bin folder of each installation", filepath.Join("installation", "bin"), "bin"},
		{"the installation folder itself, as Windows has it", "installation", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			fnm := t.TempDir()
			root := filepath.Join(fnm, "node-versions")
			bin := func(v string) string { return filepath.Join(root, v, c.versionBin) }
			for _, v := range []string{"v9.11.2", "v20.18.0", "v22.12.0"} {
				if err := os.MkdirAll(bin(v), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(filepath.Join(fnm, "aliases"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(root, "v20.18.0", "installation"), filepath.Join(fnm, "aliases", "default")); err != nil {
				t.Skipf("the default alias is a link here: %v", err)
			}
			want := []string{bin("v20.18.0"), bin("v22.12.0"), bin("v9.11.2")}
			if got := fnmBins(fnm, c.versionBin, c.aliasBin); !slices.Equal(got, want) {
				t.Fatalf("got  %q\nwant %q", got, want)
			}
		})
	}
}
