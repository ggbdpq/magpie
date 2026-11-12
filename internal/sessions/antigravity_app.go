package sessions

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/dirent"
	"github.com/yetone/magpie/internal/provider"
)

// The Antigravity app's (the desktop IDE's, agent "antigravity") own
// conversations, on the Sessions and Usage pages, read where the app keeps
// them whether or not it reached magpie's gateway (#2190, yygutn; xxooioo
// on Discord). The app keeps each conversation as a SQLite database,
// ~/.gemini/antigravity/conversations/<conversation id>.db (Insiders uses
// ~/.gemini/antigravity-insiders/conversations), written in WAL
// mode while it runs. Its messages are encrypted at rest; the figures read
// here are not. As Antigravity 2.23.0 on macOS writes them (yygutn's real
// rows on #2190; the app isn't installed on any machine magpie is tried on):
//
//	steps         idx, metadata BLOB, …: one row a step
//	gen_metadata  idx, data BLOB, …: one row a generator (a model's call)
//
// steps.metadata is a plain protobuf message:
//
//	1   the step's start (google.protobuf.Timestamp: 1 seconds, 2 nanos)
//	9   the model call's usage, on a model's step:
//	      2 input tokens, less what was read from the cache
//	      3 output tokens
//	      4 tokens written to the cache
//	      5 tokens read from the cache
//	      11 the vendor's response id
//	20  where the step sits: 3 the gen_metadata idx of its generator
//	    (absent for generator 0)
//
// gen_metadata.data's field 1 is the message asked of the model, whose
// field 19 is the model's id ("gemini-3.8-flash-n", counted as
// gemini-3.8-flash), else 21 or 22 (cc-bar's reader). Nothing else of either column is read. Each call is the app's,
// under the model its generator names; one sent through magpie is the
// gateway's to count, matched to the gateway's record by its tokens and
// time as an agy step is (usage's agyGateway: the app sends no session
// header).
//
// What the app calls a conversation is its row of
// conversation_summaries.db beside conversations/, the database agy keeps
// (agy.go): the app's language server is agy's, and Antigravity 2.29.0's
// makes the same table in ~/.gemini/antigravity on its first start. title
// is the name the app gives it, preview what it lists the conversation as
// before that (the user's own words), and workspace_uris the folders open
// when it was started, the first local one its folder on the Sessions page
// (agyFolder). Without the row, the steps' text being encrypted, a
// conversation has no name and no folder (xxooioo on Discord).
//
// A session is listed read only: the app has no command to pick a
// conversation up again from a terminal.

// AntigravityAppDir is the Antigravity app's data folder,
// ~/.gemini/antigravity.
func AntigravityAppDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gemini", "antigravity")
}

// AntigravityInsidersDir is the Insiders app's separate data folder.
func AntigravityInsidersDir() string {
	return filepath.Join(filepath.Dir(AntigravityAppDir()), "antigravity-insiders")
}

// antigravityConversations is the folder of the app's conversation databases.
func antigravityConversations() string {
	return filepath.Join(AntigravityAppDir(), "conversations")
}

// antigravityFiles are stable and Insiders' conversations. Insiders' ids
// have their own prefix so a UUID present in both stores is two sessions
// with separate summaries and call ids. Stable's existing ids stay the same.
func antigravityFiles() []file {
	out := antigravityFilesIn(antigravityConversations())
	insiders := antigravityFilesIn(filepath.Join(AntigravityInsidersDir(), "conversations"))
	for i := range insiders {
		insiders[i].key = "antigravity:insiders-" + strings.TrimPrefix(insiders[i].key, "antigravity:")
	}
	return append(out, insiders...)
}

// antigravityFilesIn are the conversation databases in dir. A file's size
// and time are the database's and its write-ahead log's together, so a
// write that is in the log alone still reads the conversation again; rev
// is its summary row, so a title the app gives it later does too.
func antigravityFilesIn(dir string) []file {
	if !Reads("antigravity") {
		return nil
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) == 0 {
		return nil
	}
	rows := agySummariesOf(filepath.Dir(dir))
	var out []file
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".db")
		if !ok || !safeID.MatchString(id) || dirent.IsFolder(dir, e) {
			continue
		}
		f := file{agent: "antigravity", key: "antigravity:" + id, path: filepath.Join(dir, e.Name()), main: true, readOnly: true}
		if !stat(&f) {
			continue
		}
		if fi, err := os.Stat(f.path + "-wal"); err == nil {
			f.size += fi.Size()
			if fi.ModTime().After(f.mod.Time()) {
				f.mod = modOf(fi.ModTime())
			}
		}
		if r, ok := rows[id]; ok {
			m := f.w()
			m.agy, m.rev = &r, agyRev(r)
		}
		out = append(out, f)
	}
	return out
}

// antigravityCallFiles are the same databases as call sources.
func antigravityCallFiles() []file { return antigravityFiles() }

// agAppCall is one model call of a conversation.
type agAppCall struct {
	step      int64
	at        time.Time
	model     string
	effort    string
	requested string
	tokens    Tokens
	reasoning int
	response  string
	execution string
}

// errAgApp is a protobuf that can't be read.
var errAgApp = errors.New("antigravity: unreadable metadata")

// antigravityRead reads a conversation's calls, in one read transaction so
// a step and its generator come from one snapshot while the app writes on.
// A call whose response id came before is the later step of that response,
// and replaces it.
func antigravityRead(path string) ([]agAppCall, error) {
	db, err := provider.OpenReadOnly(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	models := map[int64]agGen{}
	if rows, err := tx.Query("SELECT idx, data FROM gen_metadata WHERE data IS NOT NULL"); err == nil {
		for rows.Next() {
			var idx int64
			var b []byte
			if rows.Scan(&idx, &b) == nil {
				if m, e := agAppModel(b); m != "" {
					models[idx] = agGen{model: m, effort: e}
				}
			}
		}
		rows.Close()
	}
	// executor_metadata names the model each turn asked for (#6021): its
	// field 9 is the turn's execution id, the same id the step's field
	// 20.4 carries, and its field 10.1.28 the requested model's slug.
	requested := map[string]string{}
	if rows, err := tx.Query("SELECT data FROM executor_metadata WHERE data IS NOT NULL"); err == nil {
		for rows.Next() {
			var b []byte
			if rows.Scan(&b) != nil {
				continue
			}
			var exec, slug string
			_ = pbWalk(b, func(num int, v uint64, d []byte) {
				if num == 9 && d != nil {
					exec = string(d)
				}
				if num == 10 && d != nil {
					_ = pbWalk(d, func(n int, v uint64, d2 []byte) {
						if n == 1 && d2 != nil {
							_ = pbWalk(d2, func(n2 int, v2 uint64, d3 []byte) {
								if n2 == 28 && d3 != nil {
									slug = string(d3)
								}
							})
						}
					})
				}
			})
			if exec != "" && slug != "" {
				requested[exec] = slug
			}
		}
		rows.Close()
	}
	rows, err := tx.Query("SELECT idx, metadata FROM steps WHERE metadata IS NOT NULL ORDER BY idx")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []agAppCall
	byResponse := map[string]int{}
	for rows.Next() {
		var idx int64
		var b []byte
		if rows.Scan(&idx, &b) != nil {
			continue
		}
		c, gen, ok := agAppStep(b)
		if !ok {
			continue
		}
		c.step = idx
		if g := models[gen]; g.model != "" {
			c.model, c.effort = g.model, g.effort
		}
		c.requested = requested[c.execution]
		if i, seen := byResponse[c.response]; seen && c.response != "" {
			out[i] = c
			continue
		}
		if c.response != "" {
			byResponse[c.response] = len(out)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// agAppStep is a step's model call and its generator, ok when the step is
// a model's call with tokens and a time.
func agAppStep(b []byte) (c agAppCall, gen int64, ok bool) {
	var usage []byte
	err := pbWalk(b, func(num int, v uint64, d []byte) {
		switch num {
		case 1:
			if d != nil {
				c.at = pbTime(d)
			}
		case 9:
			usage = d
		case 20:
			_ = pbWalk(d, func(n int, v uint64, d []byte) {
				if n == 3 && d == nil {
					gen = int64(v)
				} else if n == 4 && d != nil {
					c.execution = string(d)
				}
			})
		}
	})
	if err != nil || usage == nil || c.at.IsZero() {
		return c, 0, false
	}
	if pbWalk(usage, func(num int, v uint64, d []byte) {
		if d == nil {
			switch num {
			case 2:
				c.tokens.Input = int(v)
			case 3:
				c.tokens.Output = int(v)
			case 4:
				c.tokens.CacheWrite = int(v)
			case 5:
				c.tokens.CacheRead = int(v)
			case 9:
				c.reasoning = int(v)
			}
		} else if num == 11 {
			c.response = string(d)
		}
	}) != nil || c.tokens.zero() {
		return c, 0, false
	}
	return c, gen, true
}

// agGen is the model a generator names, with the thinking effort its
// picker label or id carries.
type agGen struct {
	model, effort string
}

// agAppModel is the model a generator's message names, and the effort it
// was asked for (#6021): field 19, else 21, else 22. Field 19 is the
// served model's id, which may carry its effort as a suffix
// (claude-opus-5-5-high, claude-opus-4-6-thinking — catalog.Unsuffixed's
// words) or Google's serving of a base (gemini-3.8-flash-tiered,
// gemini-4-argon-trial, which the gateway names and prices as their base,
// collapseAntigravityModels). Field 21 is the picker's label, whose
// parentheses hold the level ("Gemini 4 Carbon (High)") — the level rows
// with a field 19 say nowhere else, so a bare 19 takes it from there. A
// label with no field 19 slugs to the id form ("Gemini 4 Argon (High)" is
// gemini-4-argon at high); a label of another maker keeps the version
// spelling its label has, which may differ from the id's (Claude Opus 4.6
// slugs to claude-opus-4.6, the id is claude-opus-4-6) and may then sit
// unpriced.
func agAppModel(b []byte) (string, string) {
	var named [3]string
	_ = pbWalk(b, func(num int, v uint64, d []byte) {
		if num != 1 || d == nil {
			return
		}
		_ = pbWalk(d, func(n int, v uint64, d []byte) {
			if d == nil {
				return
			}
			switch n {
			case 19:
				named[0] = strings.TrimSpace(string(d))
			case 21:
				named[1] = strings.TrimSpace(string(d))
			case 22:
				named[2] = strings.TrimSpace(string(d))
			}
		})
	})
	if m := named[0]; m != "" && len(m) <= 200 {
		model, effort := agAppModelID(m)
		if effort == "" {
			if _, level, ok := agAppLabel(named[1]); ok {
				effort = level
			}
		}
		return model, effort
	}
	for _, m := range named[1:] {
		if m == "" || len(m) > 200 {
			continue
		}
		if strings.ContainsRune(m, ' ') {
			if base, level, ok := agAppLabel(m); ok {
				return agAppSlug(base), level
			}
			return agAppSlug(m), ""
		}
		model, effort := agAppModelID(m)
		return model, effort
	}
	return "", ""
}

// agAppModelID is m as a model id: its effort suffix taken off as the
// effort (catalog.Unsuffixed's words), Google's -tiered and -trial
// servings named as their base, and a deployment's lone letter off
// (GeminiDeployment), in that order.
func agAppModelID(m string) (string, string) {
	if base, ok := catalog.Unsuffixed(m); ok {
		return agAppDeployment(base), m[len(base)+1:]
	}
	for _, w := range []string{"-tiered", "-trial"} {
		if b, ok := strings.CutSuffix(m, w); ok && strings.HasPrefix(b, "gemini-") {
			return agAppDeployment(b), ""
		}
	}
	return agAppDeployment(m), ""
}

// agAppDeployment is m with a deployment's lone letter off, when it is one.
func agAppDeployment(m string) string {
	if b, ok := GeminiDeployment(m); ok {
		return b
	}
	return m
}

// agAppLabel is a picker label's words and the level its parentheses hold:
// "Gemini 4 Carbon (High)" is "Gemini 4 Carbon" at high. False when m
// carries no parenthesized level of a word's length.
func agAppLabel(m string) (base, level string, ok bool) {
	i := strings.LastIndex(m, " (")
	if i < 0 || !strings.HasSuffix(m, ")") || len(m)-i < 4 {
		return m, "", false
	}
	level = strings.ToLower(m[i+2 : len(m)-1])
	for _, c := range level {
		if c < 'a' || c > 'z' {
			return m, "", false
		}
	}
	return m[:i], level, true
}

// agAppSlug is a label's words as an id: lower case, hyphenated ("Gemini
// 3.5 Flash" is gemini-3.5-flash). A label of the same words the id has,
// spelled as the picker shows it; another maker's version spelling may
// differ from its id's.
func agAppSlug(m string) string {
	return strings.ToLower(strings.Join(strings.Fields(m), "-"))
}

// GeminiDeployment is the Gemini model a Google deployment's name stands
// for, and whether m is such a name: a gemini-* id with one letter after
// its last hyphen. Google names the deployment serving gemini-3.8-flash
// "gemini-3.8-flash-n" (the owner's real Antigravity account, b2432754e)
// and the one serving gemini-4-argon "gemini-4-argon-d" (oioio's
// Antigravity database, 11-06): ids no request can ask for, which are that
// model, at its price. No Gemini model's own id ends in a lone letter
// (-lite, -nano, -pro, -001 and -exp are longer), and other makers' ids
// are left as written.
func GeminiDeployment(m string) (string, bool) {
	i := strings.LastIndexByte(m, '-')
	if i < 0 || len(m)-i != 2 || m[i+1] < 'a' || m[i+1] > 'z' {
		return m, false
	}
	base := m[:i]
	if !strings.HasPrefix(base, "gemini-") || len(base) == len("gemini-") {
		return m, false
	}
	return base, true
}

// pbTime is a google.protobuf.Timestamp, zero when it has no seconds.
func pbTime(b []byte) time.Time {
	var sec, nanos uint64
	_ = pbWalk(b, func(num int, v uint64, d []byte) {
		switch {
		case d != nil:
		case num == 1:
			sec = v
		case num == 2:
			nanos = v
		}
	})
	if sec == 0 || sec > 1<<40 || nanos >= 1e9 {
		return time.Time{}
	}
	return time.Unix(int64(sec), int64(nanos))
}

// pbWalk walks a protobuf message's fields, handing each to field: a
// varint's value, or a length-delimited field's bytes (non-nil). Fixed-width
// fields are skipped.
func pbWalk(b []byte, field func(num int, v uint64, d []byte)) error {
	for len(b) > 0 {
		tag, n := binary.Uvarint(b)
		if n <= 0 || tag>>3 == 0 {
			return errAgApp
		}
		b = b[n:]
		num := int(tag >> 3)
		switch tag & 7 {
		case 0:
			v, n := binary.Uvarint(b)
			if n <= 0 {
				return errAgApp
			}
			b = b[n:]
			field(num, v, nil)
		case 1:
			if len(b) < 8 {
				return errAgApp
			}
			b = b[8:]
		case 2:
			l, n := binary.Uvarint(b)
			if n <= 0 || l > uint64(len(b)-n) {
				return errAgApp
			}
			field(num, 0, b[n:n+int(l)])
			b = b[n+int(l):]
		case 5:
			if len(b) < 4 {
				return errAgApp
			}
			b = b[4:]
		default:
			return errAgApp
		}
	}
	return nil
}

// parseAntigravity reads a conversation for the Sessions page: its time and
// each model's tokens. A database that can't be read is no session yet.
func parseAntigravity(f file) *state {
	calls, err := antigravityRead(f.path)
	if err != nil {
		return &state{}
	}
	s := &state{Size: f.size, Mod: f.mod.UnixNano(), StateMore: moreOf(StateMore{DBRevision: f.m().rev})}
	if r := f.m().agy; r != nil {
		s.own().Named, s.Title, s.Cwd = title(r.Title), title(r.Preview), r.Cwd
	}
	for _, c := range calls {
		s.saw(c.at, true)
		s.use(dateOf(c.at), agAppModelOr(c.model), c.tokens)
		s.day(dateOf(c.at)).Replies++
	}
	return s
}

// agAppModelOr is the model a call is counted under: its generator's, else
// the agent's own name.
func agAppModelOr(m string) string {
	if m == "" {
		return "antigravity"
	}
	return m
}

// readAntigravityCalls reads a conversation's calls whole: the app writes
// its steps' rows over again as they finish.
func readAntigravityCalls(f file) (*callFile, bool) {
	st := &callFile{Agent: "antigravity", Path: f.path, Session: strings.TrimPrefix(f.key, "antigravity:")}
	calls, err := antigravityRead(f.path)
	if err != nil {
		return st, false
	}
	for _, c := range calls {
		st.Calls = append(st.Calls, Call{
			Agent: "antigravity", Time: c.at, Session: st.Session, Model: c.model, Tokens: c.tokens,
			Requested: c.requested, Effort: c.effort, Reasoning: c.reasoning,
			Msg: st.str("antigravity:" + st.Session + ":" + strconv.FormatInt(c.step, 10)),
		})
	}
	return st, true
}
