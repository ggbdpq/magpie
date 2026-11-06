//go:build !windows

package library

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

// SHA-1 downloads work with Git that knows neither --object-format nor GIT_DEFAULT_HASH.
func TestFetchGitLegacySHA1(t *testing.T) {
	sandbox(t)
	addr, _, _ := gitRepoFormat(t, map[string]string{"SKILL.md": "legacy"}, "sha1")
	t.Setenv("MAGPIE_TEST_REAL_GIT", realGit)
	t.Setenv("GIT_DEFAULT_HASH", "sha256")
	wrapper := filepath.Join(t.TempDir(), "git")
	testenv.Program(t, wrapper, `#!/bin/sh
for arg do
  case "$arg" in
    --object-format*)
      echo 'error: unknown option object-format' >&2
      exit 129
      ;;
  esac
done
unset GIT_DEFAULT_HASH
exec "$MAGPIE_TEST_REAL_GIT" "$@"
`)
	gitPath = func() string { return wrapper }
	dir, err := fetchGit(Source{Kind: "git", Repo: addr})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if b, err := os.ReadFile(filepath.Join(dir, "SKILL.md")); err != nil || string(b) != "legacy" {
		t.Fatalf("legacy Git contents: %q, %v", b, err)
	}
}
