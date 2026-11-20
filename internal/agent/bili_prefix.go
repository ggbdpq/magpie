package agent

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/edit"
	"gopkg.in/yaml.v3"
)

// MoveBiliPrefix rewrites only the outer billion-context prefix
// (http://127.0.0.1:<port>/bili/) on magpie's own provider URLs that already
// use one, to prefix. The rest of each URL — a custom upstream, path, and
// query — stays. User-defined providers, comments, URLs without that outer
// prefix, and profiles that have none are left untouched.
func MoveBiliPrefix(a *Agent, prefix string) error {
	if a == nil {
		return nil
	}
	if a.moveBili != nil {
		return a.moveBili(prefix)
	}
	if a.ID == "zed" {
		return zedMoveBili(a, prefix)
	}
	return nil
}

func hermesMoveBili(at place, path, prefix string) error {
	seen := map[string]bool{}
	var errs []error
	for _, p := range append([]string{path}, hermesProfiles(at)...) {
		if p == "" {
			continue
		}
		clean := filepath.Clean(p)
		if seen[clean] {
			continue
		}
		seen[clean] = true
		if _, err := os.Stat(p); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			errs = append(errs, err)
			continue
		}
		err := edit.EditYAMLNode(p, "providers", func(n *yaml.Node) (bool, bool) {
			if n == nil || n.Kind != yaml.MappingNode {
				return false, false
			}
			changed := false
			for _, name := range []string{magpieID, hermesAnthropicID} {
				v := yamlKey(yamlKey(n, name), "base_url")
				if v == nil || v.Kind != yaml.ScalarNode {
					continue
				}
				if next, ok := swapBiliPrefix(v.Value, prefix); ok {
					v.Value = next
					changed = true
				}
			}
			return changed, false
		})
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func dshMoveBili(dir, prefix string) error {
	dshWrites.Lock()
	defer dshWrites.Unlock()
	// Follow dshFiles: use profile patch lists plus the home patch and
	// settings.yaml when profiles exist; only touch legacy config.yaml when
	// no profiles exist.
	profiles := dshProfiles(dir)
	var files []string
	if len(profiles) > 0 {
		files = append(files, profiles...)
		if st, err := os.Stat(dshHomePatch(dir)); err == nil && st.Mode().IsRegular() {
			files = append(files, dshHomePatch(dir))
		}
	} else {
		legacy := filepath.Join(dir, "config.yaml")
		if st, err := os.Stat(legacy); err == nil && st.Mode().IsRegular() {
			files = append(files, legacy)
		}
	}
	seen := map[string]bool{}
	var errs []error
	for _, f := range files {
		if f == "" {
			continue
		}
		clean := filepath.Clean(f)
		if seen[clean] {
			continue
		}
		seen[clean] = true
		if err := dshMoveBiliPatchFile(f, prefix); err != nil {
			errs = append(errs, err)
		}
	}
	if len(profiles) > 0 {
		if st, err := os.Stat(dshSettings(dir)); err == nil && st.Mode().IsRegular() {
			if err := dshMoveBiliSettings(dshSettings(dir), prefix); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func dshMoveBiliPatchFile(path, prefix string) error {
	head, items, err := dshRead(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	changed := false
	if i := dshConfigIndex(items, dshPiRow); i >= 0 {
		if n, err := dshNode(items[i]); err == nil {
			providers := yamlKey(yamlKey(n, "config"), "providers")
			itemChanged := false
			for _, k := range dshKinds {
				v := yamlKey(yamlKey(providers, k.key), "baseURL")
				if v == nil || v.Kind != yaml.ScalarNode {
					continue
				}
				if next, ok := swapBiliPrefix(v.Value, prefix); ok {
					v.Value = next
					itemChanged = true
				}
			}
			if itemChanged {
				lines, err := dshNodeLines(n)
				if err != nil {
					return err
				}
				items[i].lines = lines
				changed = true
			}
		}
	}
	if dshOldWiring(items) {
		if i := dshFind(items, "llm-deepseek"); i >= 0 {
			if n, err := dshNode(items[i]); err == nil {
				v := yamlKey(yamlKey(n, "config"), "baseURL")
				if v != nil && v.Kind == yaml.ScalarNode {
					if next, ok := swapBiliPrefix(v.Value, prefix); ok {
						v.Value = next
						lines, err := dshNodeLines(n)
						if err != nil {
							return err
						}
						items[i].lines = lines
						changed = true
					}
				}
			}
		}
	}
	if !changed {
		return nil
	}
	return dshWrite(path, head, items)
}

func dshMoveBiliSettings(path, prefix string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var doc yaml.Node
	if yaml.Unmarshal(raw, &doc) != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil
	}
	return edit.EditYAMLNode(path, dshPiRow, func(n *yaml.Node) (bool, bool) {
		if n == nil || n.Kind != yaml.MappingNode {
			return false, false
		}
		providers := yamlKey(n, "providers")
		if providers == nil || providers.Kind != yaml.MappingNode {
			return false, false
		}
		changed := false
		for _, k := range dshKinds {
			v := yamlKey(yamlKey(providers, k.key), "baseURL")
			if v == nil || v.Kind != yaml.ScalarNode {
				continue
			}
			if next, ok := swapBiliPrefix(v.Value, prefix); ok {
				v.Value = next
				changed = true
			}
		}
		return changed, false
	})
}

func zedMoveBili(a *Agent, prefix string) error {
	before := ZedURL(a)
	next, ok := swapBiliPrefix(before, prefix)
	if !ok {
		return nil
	}
	if err := zedCredential(next); err != nil {
		return fmt.Errorf("configure Zed gateway credential: %w", err)
	}
	return edit.SetJSON(a.Path, edit.KV{Path: zedProvider + ".api_url", Value: next})
}

// swapBiliPrefix replaces an outer http://127.0.0.1:<port>/bili/ prefix on
// url with prefix. Everything after that prefix — the inner address, path,
// and query — stays unchanged. A URL without that outer prefix, or already
// using prefix, returns (url, false).
func swapBiliPrefix(url, prefix string) (string, bool) {
	const mark = "http://127.0.0.1:"
	s := strings.TrimSpace(url)
	rest, ok := strings.CutPrefix(s, mark)
	if !ok {
		return url, false
	}
	j := strings.Index(rest, "/bili/")
	if j <= 0 {
		return url, false
	}
	port := rest[:j]
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return url, false
	}
	old := mark + port + "/bili/"
	if old == prefix {
		return url, false
	}
	return prefix + rest[j+len("/bili/"):], true
}
