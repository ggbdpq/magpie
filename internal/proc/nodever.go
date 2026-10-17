package proc

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// The helpers here are every platform's: a version manager keeps one folder
// per Node version and a terminal runs the highest, or the version its
// default alias names, so the folders are taken in that order. What a
// platform differs in is where in a version's folder the programs sit.

// nodeVersions are the names of the version folders under root that have
// a folder bin (root/<version>/<bin>), highest version first (nodeVersionLess); a name that is
// no version (mise's "lts", "latest") after every one that is.
func nodeVersions(root, bin string) []string {
	es, _ := os.ReadDir(root)
	var names []string
	for _, e := range es {
		if st, err := os.Stat(filepath.Join(root, e.Name(), bin)); err == nil && st.IsDir() {
			names = append(names, e.Name())
		}
	}
	slices.SortStableFunc(names, func(a, b string) int {
		switch {
		case nodeVersionLess(b, a):
			return -1
		case nodeVersionLess(a, b):
			return 1
		}
		return strings.Compare(a, b)
	})
	return names
}

// parseNodeVersion reads v22.12.0 (or 22.12.0, mise's, or 22, a partial
// one) as its numbers; false for a name that isn't one.
func parseNodeVersion(s string) ([3]int, int, bool) {
	var v [3]int
	parts := strings.Split(strings.TrimPrefix(s, "v"), ".")
	if len(parts) > 3 || parts[0] == "" {
		return v, 0, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, 0, false
		}
		v[i] = n
	}
	return v, len(parts), true
}

// nodeVersionLess says a is a lower version than b, by number: v9 before
// v22. A name that is no version is lower than one that is.
func nodeVersionLess(a, b string) bool {
	va, _, oka := parseNodeVersion(a)
	vb, _, okb := parseNodeVersion(b)
	if oka != okb {
		return okb
	}
	return oka && slices.Compare(va[:], vb[:]) < 0
}

// fnmBins are the bin folders of the Node versions fnm keeps in dir, the
// default alias's (aliases/default, a link to the version's folder) first,
// then the rest newest first. versionBin is a version's bin folder under
// node-versions/<version> — installation/bin where the programs sit in a
// bin folder of the installation, installation on Windows, where they sit
// in the installation itself (npx.cmd there, no bin folder). aliasBin is
// what joins the alias's target to the same folder: "bin" there, nothing
// on Windows.
func fnmBins(dir, versionBin, aliasBin string) []string {
	root := filepath.Join(dir, "node-versions")
	var bins []string
	for _, n := range nodeVersions(root, versionBin) {
		bins = append(bins, filepath.Join(root, n, versionBin))
	}
	if to, err := filepath.EvalSymlinks(filepath.Join(dir, "aliases", "default")); err == nil {
		if real, err := filepath.EvalSymlinks(root); err == nil {
			if rel, err := filepath.Rel(real, to); err == nil && !strings.HasPrefix(rel, "..") {
				def := filepath.Join(root, rel, aliasBin)
				if isDir(def) {
					bins = slices.DeleteFunc(bins, func(b string) bool { return b == def })
					bins = append([]string{def}, bins...)
				}
			}
		}
	}
	return bins
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
