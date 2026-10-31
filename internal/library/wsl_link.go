package library

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/agent"
)

// A folder of an agent in a WSL distro may be, in the distro, a link —
// to the user's Windows folder, say — which the \\wsl.localhost share
// cannot follow or describe (#4502): lstat finds the name, stat cannot
// say what it is, a mkdir of it answers "already exists", and nothing
// under it can be reached at all. The distro itself knows where its link
// goes (readlink -f); under /mnt/<drive>/ it is a folder of Windows',
// read and written as Windows spells the path, and a link to anywhere
// else in the distro opens through the share, by the path resolved.

// wslReadlink is where the distro's link goes, as the distro says it; a
// var for tests. A distro that isn't running isn't asked, which would
// start it.
var wslReadlink = func(distro, path string) (string, error) {
	q := "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
	out, err := wslProbe(distro, "readlink -f "+q)
	return strings.TrimSpace(strings.ReplaceAll(out, "\r", "")), err
}

// wslSharePath is where a path of the distro opens: through the
// \\wsl.localhost share; a var for tests.
var wslSharePath = func(distro, linux string) string {
	return `\\wsl.localhost\` + distro + strings.ReplaceAll(linux, "/", `\`)
}

// wslDrivePath is where a path under the distro's /mnt/<drive>/ opens:
// the drive's own folder as Windows spells it; a var for tests.
var wslDrivePath = func(drive, rest string) string {
	return strings.ToUpper(drive) + ":" + strings.ReplaceAll(rest, "/", `\`)
}

// wslResolvedPath is where a path of the distro, resolved, opens: under
// /mnt/<drive>/, the drive's own folder; anything else, through the
// share (#4502).
func wslResolvedPath(distro, linux string) string {
	if rest := strings.TrimPrefix(linux, "/mnt/"); rest != linux && len(rest) > 1 && rest[1] == '/' && isDriveLetter(rest[0]) {
		return wslDrivePath(string(rest[0]), rest[1:])
	}
	return wslSharePath(distro, linux)
}

// isDriveLetter is whether b names a drive (a to z): /mnt/tools is a
// folder called tools, and /mnt/wsl WSL's own; only /mnt/c is C:.
func isDriveLetter(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// wslLink is p as magpie opens it when a folder on the way from the
// agent's home to p is a link the share cannot follow (#4502): the
// distro is asked where it goes, and the rest of the way is spelled
// there. p itself when there is no such link, when p is not the agent's
// to open, or when the distro cannot be asked: then what mkdir, read or
// write says of p as it is, is the error to keep.
func wslLink(a *agent.Agent, p string) string {
	if a == nil || a.WSL == "" || a.Home == "" || a.HomeLinux == "" {
		return p
	}
	home := filepath.Clean(a.Home)
	homeSlash := filepath.ToSlash(home)
	pSlash := filepath.ToSlash(filepath.Clean(p))
	if pSlash == homeSlash || !strings.HasPrefix(pSlash, homeSlash+"/") {
		return p
	}
	// the first folder on the way, from the home down, that lstat finds
	// and stat cannot describe, is a link the share cannot follow: what
	// lstat finds, stat cannot say what it is, and a mkdir of it answers
	// "already exists" while nothing under it can be reached at all
	link := ""
	way := pSlash[len(homeSlash):] // /a/b/c, under the home
	for at := ""; link == "" && way != ""; {
		var step string
		if i := strings.IndexByte(way[1:], '/'); i >= 0 {
			step, way = way[:i+1], way[i+1:]
		} else {
			step, way = way, ""
		}
		cur := home + filepath.FromSlash(at+step)
		if _, err := os.Lstat(cur); err != nil {
			break // not there: MkdirAll makes it, as it always has
		}
		if _, err := os.Stat(cur); err != nil {
			link = cur
			break
		}
		at += step
	}
	if link == "" {
		return p
	}
	linux := a.HomeLinux + filepath.ToSlash(link)[len(homeSlash):]
	out, err := wslReadlink(a.WSL, linux)
	if err != nil || out == "" || out == linux || !strings.HasPrefix(out, "/") {
		return p // can't tell: what mkdir says of p as it is, is kept
	}
	return filepath.FromSlash(wslResolvedPath(a.WSL, out+pSlash[len(filepath.ToSlash(link)):]))
}
