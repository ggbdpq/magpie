package gateway

// The folder an agent works in, as it tells its model in the request — for
// a group's rule by folder (provider.Rule.Folders). Each agent says it its
// own way, every format here taken from the agent's own request:
//
//   - Claude Code (2.1.296): its environment, " - Primary working
//     directory: /x", in the system prompt or a system message; when the
//     folder changes, "Primary working directory: /y (was /x)". < and > in
//     the path come as &lt; and &gt;.
//   - Codex (0.162): a user message's <environment_context> with
//     "<cwd>/x</cwd>", the path's &, < and > as entities.
//   - Pi (1.1): the system prompt's "<cwd>\n/x\n</cwd>", backslashes as /.
//   - OpenCode (2.0): "<env>\n  Working directory: /x\n…</env>", in the
//     system prompt or, when it changes, a later message.
//   - Gemini CLI (0.10–0.29): its first message's environment,
//     "I'm currently working in the directory: /x" (several roots:
//     "the following directories:" with "  - /x" lines beneath).
//     0.30 and later name them in <session_context> under
//     "- **Workspace Directories:**", each "  - /x".
//
// Of those it says, the last one goes: an agent that moved says so after.
// An agent that says none of them has no folder: a rule by folder doesn't
// match it.

import (
	"html"
	"regexp"
	"strings"
)

var (
	ccWorkDir = regexp.MustCompile(`(?m)^(?: - )?Primary working directory: (.+)$`)
	// the folder a <cwd> holds: Codex's on its line, Pi's on its own
	cwdTag = regexp.MustCompile(`<cwd>\s*([^<\n]+?)\s*</cwd>`)
	envDir = regexp.MustCompile(`(?m)^\s*Working directory: (.+)$`)
	wasDir = regexp.MustCompile(` \(was .*\)$`)
	// Gemini CLI's own line, and the heading whose "  - /x" lines hold
	// its folders (one root's older wording, several roots' older and
	// newer headings alike)
	geminiDir  = regexp.MustCompile(`(?m)^\s*I'm currently working in the directory: (.+)$`)
	geminiDirs = regexp.MustCompile(`(?m)^\s*(?:I'm currently working in the following directories:|- \*\*Workspace Directories:\*\*)\s*$`)
	dirItem    = regexp.MustCompile(`^[ \t]+- (.+)$`)
)

// workDir is the folder the request says its agent works in; "" when it
// doesn't say.
func workDir(req *Request) string {
	if req == nil {
		return ""
	}
	dir := workDirIn(req.System, "")
	for _, m := range req.Messages {
		if m.Role == "assistant" {
			continue // the model's words, not the agent's
		}
		for _, p := range m.Parts {
			if p.Kind == Text {
				dir = workDirIn(p.Text, dir)
			}
		}
	}
	return dir
}

// workDirIn is the last folder text says, else dir.
func workDirIn(text, dir string) string {
	if !strings.Contains(text, "irectory: ") && !strings.Contains(text, "<cwd>") {
		return dir
	}
	at := -1
	take := func(i int, v string) {
		if v = strings.TrimSpace(html.UnescapeString(v)); v != "" && i > at {
			at, dir = i, v
		}
	}
	for _, m := range ccWorkDir.FindAllStringSubmatchIndex(text, -1) {
		take(m[0], wasDir.ReplaceAllString(text[m[2]:m[3]], ""))
	}
	for _, m := range cwdTag.FindAllStringSubmatchIndex(text, -1) {
		take(m[0], text[m[2]:m[3]])
	}
	// OpenCode's, only within its <env>
	for rest, off := text, 0; ; {
		i := strings.Index(rest, "<env>")
		if i < 0 {
			break
		}
		j := strings.Index(rest[i:], "</env>")
		if j < 0 {
			break
		}
		block := rest[i : i+j]
		if m := envDir.FindStringSubmatchIndex(block); m != nil {
			take(off+i+m[0], block[m[2]:m[3]])
		}
		rest, off = rest[i+j:], off+i+j
	}
	return dir
}
