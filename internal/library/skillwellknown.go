package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/source"
	"gopkg.in/yaml.v3"
)

const (
	wellKnownIndexMax = 1 << 20
	wellKnownSkillMax = 1024
	wellKnownFileMax  = 8192
	wellKnownFileSize = 32 << 20
	wellKnownTimeout  = 2 * time.Minute
)

type wellKnownEntry struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Files       []string `json:"files"`
}

// probeWellKnown asks the address the user gave, without visiting its home
// page. A path without a valid index is left to git, including its sign-in.
func probeWellKnown(input string) (src Source, dir string, found bool, err error) {
	input = strings.TrimRight(strings.TrimSpace(input), "/")
	if _, ok := githubSource(input); ok {
		return Source{}, "", false, nil
	}
	u, parseErr := url.Parse(input)
	if parseErr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return Source{}, "", false, nil
	}
	if strings.EqualFold(u.Hostname(), "github.com") || strings.EqualFold(u.Hostname(), "www.github.com") {
		return Source{}, "", false, nil
	}
	for _, part := range strings.Split(strings.ToLower(u.Path), "/") {
		if strings.HasSuffix(part, ".git") {
			return Source{}, "", false, nil
		}
	}
	for _, sep := range []string{"/-/tree/", "/-/blob/", "/src/branch/", "/src/tag/", "/src/commit/"} {
		if strings.Contains(u.Path, sep) {
			return Source{}, "", false, nil
		}
	}
	base, err := wellKnownWebsite(input)
	if err != nil {
		if strings.Trim(u.Path, "/") != "" {
			return Source{}, "", false, nil
		}
		return Source{}, "", false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), wellKnownTimeout)
	defer cancel()
	client := wellKnownClient(base)
	root, entries, err := wellKnownIndex(ctx, client, base)
	if err != nil {
		if base.Path != "" {
			return Source{}, "", false, nil
		}
		return Source{}, "", false, err
	}
	src = Source{Kind: "well-known", Repo: input}
	dir, err = downloadWellKnown(ctx, client, root, entries)
	return src, dir, true, err
}

// wellKnownCandidates lists only the catalog entries. Listed SKILL.md
// templates inside an entry are supporting files, not additional skills.
func wellKnownCandidates(dir string) ([]Candidate, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]Candidate, 0, len(entries))
	for _, e := range entries {
		m, ok := readMeta(filepath.Join(dir, e.Name()))
		if !e.IsDir() || !ok {
			return nil, fmt.Errorf("%s has no valid SKILL.md", e.Name())
		}
		out = append(out, Candidate{Path: e.Name(), Name: nameFor(e.Name(), m), Description: m.Description})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// fetchWellKnown fetches the complete catalog again. Only a successful,
// fully validated fetch may say that a skill is no longer listed there.
func fetchWellKnown(src Source) (string, error) {
	base, err := wellKnownWebsite(src.Repo)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), wellKnownTimeout)
	defer cancel()
	client := wellKnownClient(base)
	root, entries, err := wellKnownIndex(ctx, client, base)
	if err != nil {
		return "", err
	}
	return downloadWellKnown(ctx, client, root, entries)
}

func wellKnownWebsite(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid skills website address: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil ||
		u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") ||
		strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return nil, fmt.Errorf("give an http or https skills website address without credentials, a query or a fragment")
	}
	escaped := strings.ToLower(u.EscapedPath())
	if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") {
		return nil, fmt.Errorf("a skills website path can't contain an escaped separator")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = strings.TrimRight(u.RawPath, "/")
	if u.Path != "" {
		for _, part := range strings.Split(strings.TrimPrefix(u.Path, "/"), "/") {
			if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\%") || strings.IndexFunc(part, unicode.IsControl) >= 0 {
				return nil, fmt.Errorf("%q isn't a safe skills website path", u.Path)
			}
		}
	}
	return u, nil
}

func wellKnownClient(base *url.URL) *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			u := req.URL
			prefix := base.Path + "/.well-known/"
			catalog, _, _ := strings.Cut(strings.TrimPrefix(via[0].URL.Path, prefix), "/")
			prefix += catalog + "/"
			if len(via) >= 10 || u.Scheme != base.Scheme || !strings.EqualFold(u.Host, base.Host) ||
				!strings.HasPrefix(u.Path, prefix) {
				return fmt.Errorf("a skills download redirected outside %s", prefix)
			}
			_, err := wellKnownWebsite(u.String())
			return err
		},
	}
}

// Each part is one decoded path segment, never a URL or a relative URL.
func wellKnownURL(base string, parts ...string) string {
	for _, part := range parts {
		base += "/" + url.PathEscape(part)
	}
	return base
}

type wellKnownStatusError struct {
	address string
	status  string
	code    int
}

func (e *wellKnownStatusError) Error() string {
	return fmt.Sprintf("%s answered %s", e.address, e.status)
}

func wellKnownGet(ctx context.Context, client *http.Client, address string, max int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "magpie")
	resp, err := source.DoOfficial(client, req)
	if err != nil {
		return nil, fmt.Errorf("couldn't fetch %s: %w", address, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &wellKnownStatusError{address: address, status: resp.Status, code: resp.StatusCode}
	}
	if max < 0 || resp.ContentLength > max {
		return nil, fmt.Errorf("%s is over the most magpie reads", address)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, fmt.Errorf("couldn't read %s: %w", address, err)
	}
	if int64(len(body)) > max {
		return nil, fmt.Errorf("%s is over the most magpie reads", address)
	}
	kind := strings.ToLower(resp.Header.Get("Content-Type"))
	sniff := http.DetectContentType(body)
	ext := strings.ToLower(filepath.Ext(req.URL.Path))
	htmlFile := ext == ".html" || ext == ".htm" || ext == ".xhtml"
	if !htmlFile && (strings.Contains(kind, "text/html") || strings.Contains(kind, "application/xhtml+xml") || strings.HasPrefix(sniff, "text/html")) {
		return nil, fmt.Errorf("%s returned an HTML page instead of a skills file", address)
	}
	return body, nil
}

func wellKnownIndex(ctx context.Context, client *http.Client, base *url.URL) (string, []wellKnownEntry, error) {
	var first error
	for _, folder := range []string{"skills", "agent-skills"} {
		root := wellKnownURL(base.String(), ".well-known", folder)
		body, err := wellKnownGet(ctx, client, root+"/index.json", wellKnownIndexMax)
		if err != nil {
			var status *wellKnownStatusError
			if !errors.As(err, &status) || (status.code != http.StatusNotFound && status.code != http.StatusGone) {
				return "", nil, err
			}
			if first == nil {
				first = err
			}
			continue
		}
		entries, err := readWellKnownIndex(body)
		if err != nil {
			return "", nil, err
		}
		return root, entries, nil
	}
	return "", nil, fmt.Errorf("couldn't read a well-known skills index at %s: %w", base, first)
}

// The whole manifest is checked before a temporary folder is made. The
// path map includes directories, so a file can't stand in for one, and
// spelling is kept to catch collisions on Windows as well as on Unix.
func readWellKnownIndex(body []byte) ([]wellKnownEntry, error) {
	if !utf8.Valid(body) {
		return nil, fmt.Errorf("the skills index isn't UTF-8 text")
	}
	var index struct {
		Skills *[]wellKnownEntry `json:"skills"`
	}
	if err := json.Unmarshal(body, &index); err != nil {
		return nil, fmt.Errorf("the skills index wasn't understood: %w", err)
	}
	if index.Skills == nil {
		return nil, fmt.Errorf("the skills index has no skills list")
	}
	entries := *index.Skills
	if len(entries) > wellKnownSkillMax {
		return nil, fmt.Errorf("the skills index lists more than %d skills", wellKnownSkillMax)
	}
	type node struct {
		path string
		dir  bool
	}
	paths := map[string]node{}
	names := map[string]bool{}
	files := 0
	for _, entry := range entries {
		if !wellKnownPath(entry.Name) || strings.Contains(entry.Name, "/") || strings.HasPrefix(entry.Name, ".") ||
			strings.EqualFold(entry.Name, "node_modules") || strings.TrimSpace(entry.Description) == "" {
			return nil, fmt.Errorf("the skills index has an invalid name or description for %q", entry.Name)
		}
		key := wellKnownKey(entry.Name)
		if names[key] {
			return nil, fmt.Errorf("the skills index repeats the name %q", entry.Name)
		}
		names[key] = true
		hasSkill := false
		for _, file := range entry.Files {
			files++
			if files > wellKnownFileMax {
				return nil, fmt.Errorf("the skills index lists more than %d files", wellKnownFileMax)
			}
			if !wellKnownPath(file) {
				return nil, fmt.Errorf("the skills index lists an unsafe file %q in %s", file, entry.Name)
			}
			hasSkill = hasSkill || file == "SKILL.md"
			parts := strings.Split(entry.Name+"/"+file, "/")
			for i := range parts {
				path := strings.Join(parts[:i+1], "/")
				key := wellKnownKey(path)
				dir := i < len(parts)-1
				if old, ok := paths[key]; ok && (old.path != path || old.dir != dir || !dir) {
					return nil, fmt.Errorf("the skills index has conflicting paths at %q", path)
				}
				paths[key] = node{path: path, dir: dir}
			}
		}
		if !hasSkill {
			return nil, fmt.Errorf("the skills index lists no SKILL.md for %s", entry.Name)
		}
	}
	return entries, nil
}

func wellKnownKey(s string) string {
	return strings.Map(func(r rune) rune {
		key := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < key {
				key = next
			}
		}
		return key
	}, s)
}

// These checks hold on every supported OS, regardless of where the index
// is read. Percent escapes aren't file names in this protocol.
func wellKnownPath(path string) bool {
	if len(path) > 1024 || !utf8.ValidString(path) || !localFile(path) ||
		strings.ContainsAny(path, "%<>\"|?*") || strings.IndexFunc(path, unicode.IsControl) >= 0 {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." || len(part) > 255 || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") ||
			strings.EqualFold(part, ".git") || strings.EqualFold(part, marker) {
			return false
		}
		stem, _, _ := strings.Cut(part, ".")
		stem = strings.ToUpper(strings.TrimRight(stem, " "))
		switch stem {
		case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
			return false
		}
		if (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) &&
			strings.Contains("|1|2|3|4|5|6|7|8|9|¹|²|³|", "|"+stem[3:]+"|") {
			return false
		}
	}
	return true
}

func downloadWellKnown(ctx context.Context, client *http.Client, root string, entries []wellKnownEntry) (dir string, err error) {
	dir, err = os.MkdirTemp("", skillTemp)
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(dir)
			dir = ""
		}
	}()
	total := int64(0)
	for _, entry := range entries {
		for _, file := range entry.Files {
			limit := min(int64(wellKnownFileSize), maxFetch-total)
			parts := append([]string{entry.Name}, strings.Split(file, "/")...)
			body, e := wellKnownGet(ctx, client, wellKnownURL(root, parts...), limit)
			if e != nil {
				return dir, e
			}
			total += int64(len(body))
			if file == "SKILL.md" {
				if e := wellKnownMeta(body); e != nil {
					return dir, fmt.Errorf("%s's SKILL.md is invalid: %w", entry.Name, e)
				}
			}
			path := filepath.Join(dir, entry.Name, filepath.FromSlash(file))
			if e := os.MkdirAll(filepath.Dir(path), 0o755); e != nil {
				return dir, e
			}
			if e := os.WriteFile(path, body, 0o644); e != nil {
				return dir, e
			}
		}
	}
	return dir, nil
}

func wellKnownMeta(body []byte) error {
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	if !utf8.ValidString(text) {
		return fmt.Errorf("not UTF-8 text")
	}
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return fmt.Errorf("no YAML front matter")
	}
	lines := strings.Split(rest, "\n")
	for i, line := range lines {
		if line != "---" {
			continue
		}
		var m meta
		if err := yaml.Unmarshal([]byte(strings.Join(lines[:i], "\n")), &m); err != nil {
			return err
		}
		if strings.TrimSpace(m.Name) == "" || strings.TrimSpace(m.Description) == "" {
			return fmt.Errorf("no name or description in the front matter")
		}
		return nil
	}
	return fmt.Errorf("unclosed YAML front matter")
}

// Websites have no commit to remember. A failed read or hash stays unknown,
// and the fetched hash accounts for a name changed when it was installed.
func checkWellKnownSkill(s *Skill, f *fetcher) SkillCheck {
	c := SkillCheck{Name: s.Name, Status: "unknown"}
	if s.Source == nil || s.Source.Kind != "well-known" || !wellKnownPath(s.Source.Path) || strings.Contains(s.Source.Path, "/") {
		c.Error = "the skill has no valid well-known source path"
		return c
	}
	src := *s.Source
	dir, err := f.fetch(src)
	if err != nil {
		c.Error = err.Error()
		return c
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		c.Error = err.Error()
		return c
	}
	listed := false
	for _, entry := range entries {
		listed = listed || entry.Name() == src.Path && entry.IsDir()
	}
	if !listed {
		return goneCheck(&GoneError{Name: s.Name, Source: src.Repo})
	}
	have := s.Hash
	if have == "" {
		have = hashDir(skillDir(s.Name))
	}
	theirs := s.fetchedHash(filepath.Join(dir, src.Path))
	if have == "" || theirs == "" {
		c.Error = "couldn't hash the skill's files to check for updates"
		return c
	}
	c.Status = "current"
	if have != theirs {
		c.Status, c.URL = "update", src.Repo
	}
	return c
}
