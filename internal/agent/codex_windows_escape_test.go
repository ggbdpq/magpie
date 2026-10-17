package agent

import (
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// tomlEscapesValid reports the first backslash inside a basic string that no
// TOML escape follows, or "" when every one is spelled as the spec has it.
// A write that left such a string behind is one Codex refuses to load
// (#2351), whatever our own parser says of the file.
func tomlEscapesValid(cfg string) string {
	for i, ln := range strings.Split(cfg, "\n") {
		inBasic, inLiteral := false, false
		for j := 0; j < len(ln); j++ {
			c := ln[j]
			switch {
			case inLiteral:
				if c == '\'' {
					inLiteral = false
				}
			case inBasic:
				switch c {
				case '\\':
					j++
					if j >= len(ln) || !strings.ContainsRune(`btnfr"\`, rune(ln[j])) {
						return ln + " (line " + itoaLine(i+1) + ")"
					}
				case '"':
					inBasic = false
				}
			case c == '#':
				j = len(ln)
			case c == '"':
				inBasic = true
			case c == '\'':
				inLiteral = true
			}
		}
	}
	return ""
}

func itoaLine(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// checkTOML parses cfg and refuses an escape no TOML parser reads.
func checkTOML(t *testing.T, cfg string) {
	t.Helper()
	var doc map[string]any
	if err := toml.Unmarshal([]byte(cfg), &doc); err != nil {
		t.Fatalf("no longer valid TOML: %v\n%s", err, cfg)
	}
	if bad := tomlEscapesValid(cfg); bad != "" {
		t.Fatalf("an escape TOML has no name for: %s\n%s", bad, cfg)
	}
}

// The config the way Codex itself writes it on Windows: a path in every
// string a trust, a server or a notify names, escaped as its serializer
// does. A pick through magpie, and the one after it (what a fresh magpie
// does again at its first sync), leave every escape as it was.
func TestCodexPickKeepsTheEscapesAWindowsConfigHolds(t *testing.T) {
	const config = `model = "gpt-5.5"
notify = ["C:\\Program Files\\Codex\\notifier.exe", "turn-ended"]

[projects."C:\\Users\\me\\proj"]
trust_level = "trusted"

[mcp_servers.filesystem]
command = "C:\\Users\\me\\AppData\\Roaming\\npm\\node.exe"
args = ["-y", "server-filesystem", "C:\\Users\\me\\docs"]

[mcp_servers.filesystem.env]
DOCS = "C:\\Users\\me\\docs"

[hooks.state."C:\\Users\\me\\.codex\\hooks.json:stop:0:0"]
trusted_hash = "sha256:aaa"

[desktop]
enabled-reasoning-efforts = ["medium", "high"]
`
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, config)
	checkTOML(t, read())
	for i := 0; i < 2; i++ {
		if err := codex(home).Fields[0].Set("fake/m1"); err != nil {
			t.Fatalf("pick: %v\n%s", err, read())
		}
		cfg := read()
		checkTOML(t, cfg)
		for _, keep := range []string{
			`[projects."C:\\Users\\me\\proj"]`,
			`command = "C:\\Users\\me\\AppData\\Roaming\\npm\\node.exe"`,
			`DOCS = "C:\\Users\\me\\docs"`,
			`[hooks.state."C:\\Users\\me\\.codex\\hooks.json:stop:0:0"]`,
			`notify = ["C:\\Program Files\\Codex\\notifier.exe", "turn-ended"]`,
		} {
			if !strings.Contains(cfg, keep) {
				t.Errorf("%q is not as it was:\n%s", keep, cfg)
			}
		}
	}
}
