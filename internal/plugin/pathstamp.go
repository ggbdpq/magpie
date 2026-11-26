package plugin

import (
	"fmt"
	"io/fs"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// A plugin added by its path (a file or folder of the user's own, as a
// plugin's author has it while writing it) is loaded by the host as it
// was when the host started. An edit to it went unseen until magpie was
// quit and opened again (yansheng1003 on X: "每次改了需要重新magpie"), so
// what the host loaded is looked at as plugins.json is (checkList): once
// its files change, the host is started again with them, as the
// middleware already is (middleware.stamp).

// pathsEvery is how often the path plugins' files are looked at: checkList
// runs on every call to a plugin, and a folder is walked.
var pathsEvery = time.Second

// pathLoopLimit and pathLoopWindow bound automatic path-watch reloads for
// any single local path plugin: a plugin that writes runtime files inside
// its own directory would otherwise restart the shared host forever (#7198).
var (
	pathLoopLimit  = 5
	pathLoopWindow = time.Minute
	pathLoopNow    = time.Now
)

// pathsMax bounds the files looked at in one plugin's folder: a path
// plugin pointed at a large tree is seen changing in its first files only.
const pathsMax = 2000

type pathSnap struct {
	list  string
	stamp string
	specs map[string]string
	files map[string]map[string]string
}

var pathsSeen struct {
	sync.Mutex
	at      time.Time
	list    string // plugins.json's stamp the paths were found under
	snap    pathSnap
	reloads map[string][]time.Time // recent path-triggered reloads by spec
	paused  map[string]bool        // specs paused by the restart-loop circuit breaker
}

// seenStamp is what the host loaded the plugins from: plugins.json and
// what is installed (listStamp), and the files of the plugins added by
// their path.
func seenStamp() string {
	s := pathsSnapshot()
	return s.list + "|" + s.stamp
}

// pathsStamp is the files of every plugin on in plugins.json that was
// added by its path: their names, times and sizes. Walked at most once a
// pathsEvery, unless plugins.json changed since.
func pathsStamp() string { return pathsSnapshot().stamp }

// pathsSnapshot walks enabled path plugins (respecting pathsEvery and
// per-plugin loop-breaker pauses) and returns both the combined stamp and
// per-spec details so checkList can log triggers and break self-reloading
// loops (#7198).
func pathsSnapshot() pathSnap {
	ls := listStamp()
	pathsSeen.Lock()
	defer pathsSeen.Unlock()
	if pathsSeen.list != ls {
		pathsSeen.reloads = nil
		pathsSeen.paused = nil
	} else if time.Since(pathsSeen.at) < pathsEvery {
		return pathsSeen.snap
	}
	var all strings.Builder
	specs := map[string]string{}
	files := map[string]map[string]string{}
	for _, e := range list().Plugins {
		if e.Off || !IsPath(e.Spec) {
			continue
		}
		if pathsSeen.paused[e.Spec] {
			specs[e.Spec] = pathsSeen.snap.specs[e.Spec]
			files[e.Spec] = pathsSeen.snap.files[e.Spec]
			all.WriteString(specs[e.Spec])
			continue
		}
		var b strings.Builder
		fm := map[string]string{}
		stampPath(&b, Target(e.Spec), fm)
		s := b.String()
		specs[e.Spec] = s
		files[e.Spec] = fm
		all.WriteString(s)
	}
	pathsSeen.at = time.Now()
	pathsSeen.list = ls
	pathsSeen.snap = pathSnap{list: ls, stamp: all.String(), specs: specs, files: files}
	return pathsSeen.snap
}

// resetPathLoops clears the per-plugin path restart-loop state so an
// explicit Restart or plugins.json change resumes auto-reloading.
func resetPathLoops() {
	pathsSeen.Lock()
	pathsSeen.reloads = nil
	pathsSeen.paused = nil
	pathsSeen.at = time.Time{}
	pathsSeen.Unlock()
}

// allowPathReload logs the file that triggered a path plugin reload and
// pauses auto-reload for spec if it exceeds pathLoopLimit within
// pathLoopWindow (#7198).
func allowPathReload(spec, changedPath string) bool {
	now := pathLoopNow()
	cutoff := now.Add(-pathLoopWindow)
	pathsSeen.Lock()
	defer pathsSeen.Unlock()
	if pathsSeen.paused[spec] {
		return false
	}
	if pathsSeen.reloads == nil {
		pathsSeen.reloads = map[string][]time.Time{}
	}
	kept := pathsSeen.reloads[spec][:0]
	for _, t := range pathsSeen.reloads[spec] {
		if !t.Before(cutoff) {
			kept = append(kept, t)
		}
	}
	kept = append(kept, now)
	pathsSeen.reloads[spec] = kept
	if len(kept) > pathLoopLimit {
		if pathsSeen.paused == nil {
			pathsSeen.paused = map[string]bool{}
		}
		pathsSeen.paused[spec] = true
		log.Printf("plugin: paused auto-reload for %s after %d reloads in %s (last changed: %s); toggle the plugin off and on or restart magpie to reload it", spec, pathLoopLimit, pathLoopWindow, changedPath)
		return false
	}
	log.Printf("plugin: path plugin %s changed (%s); reloading host", spec, changedPath)
	return true
}

// firstChangedFile names the first added, removed, or modified file between
// prev and cur in sorted path order.
func firstChangedFile(prev, cur map[string]string) string {
	seen := map[string]bool{}
	for p := range prev {
		seen[p] = true
	}
	for p := range cur {
		seen[p] = true
	}
	for _, p := range slices.Sorted(maps.Keys(seen)) {
		if prev[p] != cur[p] {
			return p
		}
	}
	return ""
}

// stampPath writes root's files to b: root itself when it is a file, else
// what is under it, its dependencies (node_modules) and hidden folders
// (.git) left out.
func stampPath(b *strings.Builder, root string, files map[string]string) {
	n := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable: as it is, nothing to compare
		}
		if d.IsDir() {
			if p != root && (d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if n++; n > pathsMax {
			return filepath.SkipAll
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			if t, err := os.Stat(p); err == nil {
				fi = t
			}
		}
		val := fmt.Sprintf("%d:%d", fi.ModTime().UnixNano(), fi.Size())
		if files != nil {
			files[p] = val
		}
		fmt.Fprint(b, p, ":", val, ";")
		return nil
	})
}
