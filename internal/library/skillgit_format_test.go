package library

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Downloads preserve the requested ref's contents, including tags and commits off the default branch.
func TestFetchGitObjectFormats(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			sandbox(t)
			addr, commit, run := gitRepoFormat(t, map[string]string{"SKILL.md": "v1"}, format)
			pinned := run("rev-parse", "HEAD")
			run("tag", "-a", "v1", "-m", "v1")
			run("branch", "old")
			run("push", "-q", addr, "refs/tags/v1", "refs/heads/old")
			commit(map[string]string{"SKILL.md": "v2"})
			if format == "sha256" {
				t.Setenv("GIT_DEFAULT_HASH", "sha1")
			} else {
				t.Setenv("GIT_DEFAULT_HASH", "sha256")
			}
			run("config", "--global", "init.defaultObjectFormat", os.Getenv("GIT_DEFAULT_HASH"))
			for _, c := range []struct{ name, ref, body string }{
				{"default", "", "v2"}, {"branch", "old", "v1"},
				{"tag", "v1", "v1"}, {"commit", pinned, "v1"},
				{"unborn-head", "main", "v2"},
			} {
				t.Run(c.name, func(t *testing.T) {
					if c.name == "unborn-head" {
						run("-C", "../skills.git", "symbolic-ref", "HEAD", "refs/heads/unborn")
					}
					dir, err := fetchGit(Source{Kind: "git", Repo: addr, Ref: c.ref})
					if err != nil {
						t.Fatalf("fetching %s %s: %v", format, c.name, err)
					}
					defer os.RemoveAll(dir)
					if b, err := os.ReadFile(filepath.Join(dir, "SKILL.md")); err != nil || string(b) != c.body {
						t.Fatalf("fetched contents: %q, %v; want %q", b, err, c.body)
					}
					if got, want := gitHeadOf(dir), run("rev-parse", defaultRef(c.ref)+"^{commit}"); got != want {
						t.Fatalf("fetched commit = %q, want %q", got, want)
					}
					if _, err := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
						t.Fatalf("download kept .git: %v", err)
					}
				})
			}
			if dir, err := fetchGit(Source{Kind: "git", Repo: addr, Ref: "missing"}); err == nil || dir != "" || !strings.Contains(err.Error(), "no branch or tag missing") {
				t.Fatalf("missing ref: dir=%q err=%v", dir, err)
			}
		})
	}
}

// Malformed output is neither an empty repository nor a valid abbreviated object ID.
func TestGitRemoteRefsValidation(t *testing.T) {
	src := Source{Repo: "file:///fixture.git"}
	for _, length := range []int{6, 7, 39, 40, 41, 63, 64, 65} {
		oid := strings.Repeat("a", length)
		_, err := parseGitRemoteRefs(src, oid+"\tHEAD\n")
		valid := length == 40 || length == 64
		if (err == nil) != valid {
			t.Errorf("%d-digit remote object ID: %v", length, err)
		}
	}
	sha1, sha256 := strings.Repeat("a", 40), strings.Repeat("b", 64)
	for _, out := range []string{
		strings.Repeat("g", 40) + "\tHEAD\n", sha1 + " HEAD\n", sha1 + "\t\n",
		sha1 + "\tHEAD\n" + sha256 + "\trefs/heads/main\n",
	} {
		if _, err := parseGitRemoteRefs(src, out); err == nil {
			t.Errorf("accepted invalid remote output %q", out)
		}
	}
	refs, err := parseGitRemoteRefs(src, strings.ToUpper(sha256)+"\tHEAD\n")
	if err != nil || refs["HEAD"] != sha256 {
		t.Fatalf("full object ID normalization: %+v, %v", refs, err)
	}
	refs, err = parseGitRemoteRefs(src, "")
	if err != nil || len(refs) != 0 {
		t.Fatalf("empty refs: %+v, %v", refs, err)
	}
}

// An empty repository provides no format; fetching fails without leaving a download directory.
func TestFetchGitEmptyRemote(t *testing.T) {
	sandbox(t)
	addr, _, run := gitRepoFormat(t, map[string]string{"SKILL.md": "fixture"}, "sha256")
	run("-C", "../skills.git", "update-ref", "-d", "refs/heads/main")
	before := strings.Join(leftDownloads(os.TempDir()), "\n")
	dir, err := fetchGit(Source{Kind: "git", Repo: addr})
	if dir != "" || err == nil || !strings.Contains(err.Error(), "couldn't determine its object format") {
		t.Fatalf("empty repository treated as a known format: dir=%q err=%v", dir, err)
	}
	if after := strings.Join(leftDownloads(os.TempDir()), "\n"); after != before {
		t.Fatalf("failed fetch left download directories: %s", after)
	}
}

// Format probing shares the download's timeout budget.
func TestFetchGitProbeTimeout(t *testing.T) {
	sandbox(t)
	gitRepo(t, map[string]string{"SKILL.md": "fixture"})
	previous := gitTimeout
	gitTimeout = time.Second
	defer func() { gitTimeout = previous }()
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-done:
		}
	}))
	defer srv.Close()
	defer close(done)
	before := strings.Join(leftDownloads(os.TempDir()), "\n")
	dir, err := fetchGit(Source{Kind: "git", Repo: srv.URL})
	if dir != "" || err == nil || !strings.Contains(err.Error(), "was stopped") {
		t.Fatalf("probe wasn't stopped on timeout: dir=%q err=%v", dir, err)
	}
	if after := strings.Join(leftDownloads(os.TempDir()), "\n"); after != before {
		t.Fatalf("timeout left download directories: %s", after)
	}
}

func defaultRef(ref string) string {
	if ref == "" {
		return "HEAD"
	}
	return ref
}

// Hexadecimal branch and tag names follow the remote rather than being mistaken for pinned commits.
func TestGitHexNamedRefsFollowRemote(t *testing.T) {
	sandbox(t)
	addr, commit, run := gitRepoFormat(t, map[string]string{"SKILL.md": "v1"}, "sha1")
	for _, length := range []int{7, 40, 45, 64} {
		for _, kind := range []string{"heads", "tags"} {
			ref := strings.Repeat("a", length)
			remoteRef := "refs/" + kind + "/" + ref
			run("push", "-q", addr, "HEAD:"+remoteRef)
			want := run("rev-parse", "HEAD")
			src := Source{Kind: "git", Repo: addr, Ref: ref}
			if got, err := gitRemoteHead(src); err != nil || got != want {
				t.Fatalf("%s: head=%q err=%v, want %q", remoteRef, got, err, want)
			}
			commit(map[string]string{"SKILL.md": kind + ref})
			run("push", "-q", "--force", addr, "HEAD:"+remoteRef)
			want = run("rev-parse", "HEAD")
			if got, err := gitRemoteHead(src); err != nil || got != want {
				t.Fatalf("updated %s: head=%q err=%v, want %q", remoteRef, got, err, want)
			}
			run("push", "-q", addr, ":"+remoteRef)
		}
	}
}

// Remote object IDs and pinned commits both support SHA-256.
func TestGitRemoteHeadSHA256(t *testing.T) {
	sandbox(t)
	addr, _, run := gitRepoFormat(t, map[string]string{"SKILL.md": "v1"}, "sha256")
	want := run("rev-parse", "HEAD")
	run("tag", "-a", "v1", "-m", "v1")
	run("push", "-q", addr, "refs/tags/v1")
	for _, ref := range []string{"", "main", "v1", strings.ToUpper(want)} {
		head, err := gitRemoteHead(Source{Kind: "git", Repo: addr, Ref: ref})
		if err != nil || head != want {
			t.Errorf("ref %q: head=%q err=%v, want %q", ref, head, err, want)
		}
	}
}

// Format probing preserves the fallback to a full fetch over dumb HTTP.
func TestFetchGitDumbHTTP(t *testing.T) {
	sandbox(t)
	_, _, run := gitRepoFormat(t, map[string]string{"SKILL.md": "fixture"}, "sha1")
	run("-C", "../skills.git", "update-server-info")
	bare := filepath.Join(filepath.Dir(filepath.Dir(run("rev-parse", "--absolute-git-dir"))), "skills.git")
	srv := httptest.NewServer(http.FileServer(http.Dir(bare)))
	defer srv.Close()
	t.Setenv("GIT_DEFAULT_HASH", "sha256")
	dir, err := fetchGit(Source{Kind: "git", Repo: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if b, err := os.ReadFile(filepath.Join(dir, "SKILL.md")); err != nil || string(b) != "fixture" {
		t.Fatalf("dumb HTTP contents: %q, %v", b, err)
	}
}
