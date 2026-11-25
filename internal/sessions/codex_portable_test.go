package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/claudeseal"
	"github.com/yetone/magpie/internal/codexsummary"
	"github.com/yetone/magpie/internal/filelock"
)

const portThread = "01a15820-ff81-7840-bb3f-e18771c53aa8"

// portRolloutLines is a Codex 0.162 rollout of a thread magpie compacted,
// in the shapes Codex writes (codex-rs/history/src/rollout_payload.rs,
// CompactedItemWire; the compaction item as magpie's gateway streams it):
// a session_meta, a user message that talks about "magpie1:" (text, never
// changed), magpie's compaction item as a response_item, the compacted
// checkpoint holding it in replacement_history with its metadata beside,
// a checkpoint with an unreadable one of magpie's, OpenAI's own sealed
// compaction, and a later turn.
func portRolloutLines(summary string) []string {
	enc := codexsummary.Encrypted(summary)
	return []string{
		`{"timestamp":"2026-10-20T07:05:20.118Z","type":"session_meta","payload":{"id":"` + portThread + `","timestamp":"2026-10-20T07:05:20.101Z","cwd":"/Users/me/w","originator":"codex_cli_rs","cli_version":"0.162.1","source":"cli","model_provider":"openai"}}`,
		`{"timestamp":"2026-10-20T07:05:31.002Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"why does \"magpie1:\" show in my rollout? <b>&</b>"}]}}`,
		`{"timestamp":"2026-10-20T07:40:02.551Z","type":"response_item","payload":{"type":"compaction","id":"cmp_magpie_1761000002","encrypted_content":"` + enc + `"},"metadata":{"compaction_model_hash":"h1"}}`,
		`{"timestamp":"2026-10-20T07:40:02.553Z","type":"compacted","payload":{"message":"","replacement_history":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions instructions>"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the build"}]},{"type":"compaction","id":"cmp_magpie_1761000002","encrypted_content":"` + enc + `"}],"replacement_history_metadata":[{"client_authored":true},{},{"compaction_model_hash":"h1"}],"window_number":2,"compaction_response_id":"resp_magpie_1761000002","latest_token_usage_record":{"input_tokens":182734,"output_tokens":1204}}}`,
		`{"timestamp":"2026-10-20T08:10:00.000Z","type":"compacted","payload":{"message":"","replacement_history":[{"type":"message","role":"user","content":[{"type":"input_text","text":"go on"}]},{"type":"compaction","encrypted_content":"magpie1:%%%"}],"replacement_history_metadata":[{"client_authored":true},{"compaction_model_hash":"h2"}]}}`,
		`{"timestamp":"2026-10-20T09:00:00.000Z","type":"response_item","payload":{"type":"compaction","id":"cmp_0a1b","encrypted_content":"gAAAAABo1x2y3z_OpenAIsOwn=="}}`,
		`{"timestamp":"2026-10-20T09:00:05.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done: 1.5e+300 tokens, <ok>"}]}}`,
	}
}

// portHome is a Codex home with thread-writer-locks, as Codex 0.162 keeps
// it, and the rollout at sessions/2026/10/20, plain or packed, last
// written an hour ago with mode 0640.
func portHome(t *testing.T, lines []string, zst bool) (home, path string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	home = filepath.Join(t.TempDir(), ".codex")
	if err := os.MkdirAll(filepath.Join(home, "thread-writer-locks"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "sessions", "2026", "10", "20")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(dir, "rollout-2026-10-20T15-05-20-"+portThread+".jsonl")
	b := []byte(strings.Join(lines, "\n") + "\n")
	if zst {
		path += ".zst"
		z, _ := zstd.NewWriter(nil)
		b = z.EncodeAll(b, nil)
	}
	if err := os.WriteFile(path, b, 0o640); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	return home, path
}

func readRollout(t *testing.T, path string) []string {
	t.Helper()
	in, err := openLines(path)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	var buf bytes.Buffer
	buf.ReadFrom(in)
	return strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
}

func decode(t *testing.T, line string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(line), &v); err != nil {
		t.Fatalf("%v: %s", err, line)
	}
	return v
}

// LukeMa (X): a Codex conversation magpie compacted errored once magpie
// was quit, as OpenAI can't read magpie's compaction item
// (invalid_encrypted_content). PortCodexRollouts writes each of magpie's
// items as Codex's own summary message, the one the gateway sends for it,
// and changes nothing else: the other lines keep their bytes, the file
// its line count, mode and time, and a compacted line's metadata stays
// beside its items.
func TestPortCodexRollouts(t *testing.T) {
	for _, zst := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "zst"}[zst], func(t *testing.T) {
			summary := "Progress: build fixed in internal/x.\nNext: run the suite."
			lines := portRolloutLines(summary)
			home, path := portHome(t, lines, zst)
			before, _ := os.Stat(path)

			res, err := PortCodexRollouts(context.Background(), home)
			if err != nil || !res.Done || len(res.Rewritten) != 1 || len(res.Held) != 0 {
				t.Fatalf("pass: %+v, %v", res, err)
			}
			after, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if !after.ModTime().Equal(before.ModTime()) || after.Mode() != before.Mode() {
				t.Errorf("time/mode %v %v, was %v %v", after.ModTime(), after.Mode(), before.ModTime(), before.Mode())
			}
			got := readRollout(t, path)
			if len(got) != len(lines) {
				t.Fatalf("%d lines, was %d", len(got), len(lines))
			}
			for _, i := range []int{0, 1, 5, 6} {
				if got[i] != lines[i] {
					t.Errorf("line %d changed:\n%s\nwas\n%s", i, got[i], lines[i])
				}
			}
			want := codexsummary.UserMessage(codexsummary.Prefix + "\n" + summary)
			wantJSON, _ := json.Marshal(want)
			var wantAny any
			json.Unmarshal(wantJSON, &wantAny)

			l2 := decode(t, got[2])
			if !reflect.DeepEqual(l2["payload"], wantAny) {
				t.Errorf("response_item payload %v, want %v", l2["payload"], wantAny)
			}
			if !reflect.DeepEqual(l2["metadata"], map[string]any{"compaction_model_hash": "h1"}) || l2["type"] != "response_item" {
				t.Errorf("the rest of the line changed: %s", got[2])
			}
			p3 := decode(t, got[3])["payload"].(map[string]any)
			hist := p3["replacement_history"].([]any)
			if len(hist) != 3 || !reflect.DeepEqual(hist[2], wantAny) {
				t.Errorf("replacement_history %v", hist)
			}
			if meta := p3["replacement_history_metadata"].([]any); len(meta) != 3 {
				t.Errorf("metadata %v", meta)
			}
			if p3["window_number"] != float64(2) || p3["compaction_response_id"] != "resp_magpie_1761000002" {
				t.Errorf("checkpoint fields changed: %s", got[3])
			}
			// the unreadable one is left out with its metadata entry, as the
			// gateway leaves it out of what it sends
			p4 := decode(t, got[4])["payload"].(map[string]any)
			if h := p4["replacement_history"].([]any); len(h) != 1 {
				t.Errorf("replacement_history %v", h)
			}
			if m := p4["replacement_history_metadata"].([]any); len(m) != 1 || !reflect.DeepEqual(m[0], map[string]any{"client_authored": true}) {
				t.Errorf("metadata %v", m)
			}
			// nothing of magpie's that OpenAI would refuse is left
			for i, l := range got {
				if i != 1 && strings.Contains(l, codexsummary.Mark) {
					t.Errorf("line %d still has magpie's item: %s", i, l)
				}
			}
			// a second pass has nothing to do
			if res, err := PortCodexRollouts(context.Background(), home); err != nil || len(res.Rewritten) != 0 {
				t.Errorf("second pass: %+v, %v", res, err)
			}
		})
	}
}

// #4735: a pass over a rollout with nothing of magpie's in it only reads
// it. It wrote each one out again, compressed for a .zst, and threw that
// away, which cost a pass more than the read. A line longer than the read
// buffer is read whole, and one of magpie's after it is still written.
func TestPortCodexRolloutsOnlyReadsWhatHasNothingToChange(t *testing.T) {
	long := `{"timestamp":"2026-10-20T07:06:00.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"` + strings.Repeat("x", 300<<10) + `"}]}}`
	for _, zst := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "zst"}[zst], func(t *testing.T) {
			all := portRolloutLines("Progress: done.")
			lines := []string{all[0], all[1], long, all[5], all[6]}
			home, path := portHome(t, lines, zst)
			before, _ := os.ReadFile(path)
			was := rolloutWrites.Load()
			res, err := PortCodexRollouts(context.Background(), home)
			if err != nil || !res.Done || len(res.Rewritten) != 0 {
				t.Fatalf("pass: %+v, %v", res, err)
			}
			if n := rolloutWrites.Load() - was; n != 0 {
				t.Errorf("%d rollouts written out for nothing", n)
			}
			if after, _ := os.ReadFile(path); !bytes.Equal(after, before) {
				t.Error("the rollout changed")
			}

			lines = []string{all[0], all[1], long, all[2], all[6]}
			home, path = portHome(t, lines, zst)
			res, err = PortCodexRollouts(context.Background(), home)
			if err != nil || len(res.Rewritten) != 1 {
				t.Fatalf("pass: %+v, %v", res, err)
			}
			got := readRollout(t, path)
			if len(got) != len(lines) || got[2] != long || got[4] != lines[4] || strings.Contains(got[3], codexsummary.Mark) {
				t.Errorf("rewritten: %d lines, long kept %v, item %.200s", len(got), len(got) > 2 && got[2] == long, got[min(3, len(got)-1)])
			}
		})
	}
}

// A rollout whose thread a Codex has open (its writer lock held) is never
// written under it: it is left as it was, kept for a later pass, and
// written once the Codex lets it go. A Codex home without
// thread-writer-locks, where there is no telling, is left alone.
func TestPortCodexRolloutsLeavesAnOpenThread(t *testing.T) {
	lines := portRolloutLines("s")
	home, path := portHome(t, lines, false)
	orig, _ := os.ReadFile(path)
	lf, err := os.OpenFile(filepath.Join(home, "thread-writer-locks", portThread+".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer lf.Close()
	if got, err := filelock.TryLock(lf); !got || err != nil {
		t.Fatalf("lock: %v %v", got, err)
	}
	res, err := PortCodexRollouts(context.Background(), home)
	if err != nil || len(res.Held) != 1 || len(res.Rewritten) != 0 {
		t.Fatalf("pass with the thread open: %+v, %v", res, err)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, orig) {
		t.Fatal("written while a Codex had the thread open")
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".magpie-*")); len(left) > 0 {
		t.Fatalf("left behind: %v", left)
	}
	filelock.Unlock(lf)
	// the pass after looks again at what it left, though its time is old
	res, err = PortCodexRollouts(context.Background(), home)
	if err != nil || len(res.Rewritten) != 1 {
		t.Fatalf("pass after: %+v, %v", res, err)
	}

	home2, path2 := portHome(t, lines, false)
	os.Remove(filepath.Join(home2, "thread-writer-locks"))
	orig2, _ := os.ReadFile(path2)
	if res, err := PortCodexRollouts(context.Background(), home2); err != nil || len(res.Rewritten) != 0 {
		t.Fatalf("no writer locks: %+v, %v", res, err)
	}
	if b, _ := os.ReadFile(path2); !bytes.Equal(b, orig2) {
		t.Fatal("written without Codex's writer locks")
	}
}

// A line is parsed only for magpie's item; text that only mentions
// "magpie1:" keeps its bytes, and a line that is one unreadable item of
// magpie's alone stays (leaving it out would move the lines after it).
func TestPortLineLeavesOthers(t *testing.T) {
	for _, l := range []string{
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"magpie1:abc"}]}}` + "\n",
		`{"type":"response_item","payload":{"type":"compaction","encrypted_content":"magpie1:%%%"}}` + "\r\n",
		`{"type":"response_item","payload":{"type":"reasoning","encrypted_content":"magpie1:xyz"}}` + "\n",
		`not json magpie1:` + "\n",
	} {
		if got, ok := portLine([]byte(l)); ok || string(got) != l {
			t.Errorf("%q became %q", l, got)
		}
	}
	l := `{"type":"response_item","payload":{"type":"compaction_summary","encrypted_content":"` + codexsummary.Encrypted("x") + `"}}` + "\r\n"
	got, ok := portLine([]byte(l))
	if !ok || !bytes.HasSuffix(got, []byte("}\r\n")) || bytes.Contains(got, []byte("magpie1:")) {
		t.Errorf("compaction_summary: %q", got)
	}
}

// A reasoning item carrying Claude's block (claudeseal) is written as its
// summary, as the gateway sends it to OpenAI, in a response_item and in a
// compacted line's replacement_history; OpenAI's own encrypted_content and
// a mention of the mark in text keep their bytes.
func TestPortLineDropsClaudeBlocks(t *testing.T) {
	sealed := claudeseal.Seal([]byte(`{"type":"thinking","thinking":"hm","signature":"sig"}`))
	for _, l := range []string{
		`{"type":"response_item","payload":{"type":"reasoning","summary":[],"encrypted_content":"gAAAAopenai"}}` + "\n",
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"` + sealed + `"}]}}` + "\n",
	} {
		if got, ok := portLine([]byte(l)); ok || string(got) != l {
			t.Errorf("%q became %q", l, got)
		}
	}
	for _, l := range []string{
		`{"type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"hm"}],"encrypted_content":"` + sealed + `"}}` + "\n",
		`{"type":"compacted","payload":{"message":"","replacement_history":[{"type":"reasoning","summary":[],"encrypted_content":"` + sealed + `"}]}}` + "\n",
	} {
		got, ok := portLine([]byte(l))
		if !ok || bytes.Contains(got, []byte(claudeseal.Mark)) || bytes.Contains(got, []byte("encrypted_content")) || !bytes.Contains(got, []byte(`"reasoning"`)) {
			t.Errorf("%q became %q", l, got)
		}
	}
}

// lineEnds is where each line of a rollout ends, newline included: the
// places Codex keeps by byte (history_base.end_byte_offset, the state
// database's turn and projection offsets).
func lineEnds(t *testing.T, path string) []int {
	t.Helper()
	in, err := openLines(path)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	var buf bytes.Buffer
	buf.ReadFrom(in)
	var ends []int
	for i, c := range buf.Bytes() {
		if c == '\n' {
			ends = append(ends, i+1)
		}
	}
	return ends
}

// xybio (Discord): the Codex app wouldn't reopen threads ("invalid
// paginated history lineage for …: cutoff byte offset is past the source
// rollout"). A fork or a revert names the byte its parent's history ends
// at (session_meta.history_base), and Codex's state database keeps turns
// and how far it has read by byte too; a ported line, a summary message
// shorter than magpie's base64 item, made the parent shorter, so every
// such place moved or went past its end. A rollout written portable keeps
// every line where it was, and a fork's place in it stays a line's end.
func TestPortCodexRolloutsKeepsBytePlaces(t *testing.T) {
	for _, zst := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "zst"}[zst], func(t *testing.T) {
			summary := strings.Repeat("Progress: build fixed in internal/x; next the suite. ", 80)
			lines := portRolloutLines(summary)
			lines[0] = strings.Replace(lines[0], `"model_provider":"openai"`, `"model_provider":"openai","history_mode":"paginated"`, 1)
			sealed := claudeseal.Seal([]byte(`{"type":"thinking","thinking":"hm","signature":"` + strings.Repeat("s", 600) + `"}`))
			lines = append(lines, `{"timestamp":"2026-10-20T09:00:06.000Z","type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"hm"}],"encrypted_content":"`+sealed+`"}}`)
			home, path := portHome(t, lines, zst)
			before := lineEnds(t, path)

			// a fork of the thread made at its end, as the Codex app makes
			// one from where it has read to
			child := "01a1430d-e3a9-7a72-a466-071d2e3dd46b"
			cpath := filepath.Join(filepath.Dir(path), "rollout-2026-10-20T18-00-00-"+child+".jsonl")
			meta := `{"timestamp":"2026-10-20T10:00:00.000Z","type":"session_meta","payload":{"id":"` + child + `","history_mode":"paginated","history_base":{"thread_id":"` + portThread + `","end_ordinal_exclusive":` + strconv.Itoa(len(lines)) + `,"end_byte_offset":` + strconv.Itoa(before[len(before)-1]) + `}}}` + "\n"
			if err := os.WriteFile(cpath, []byte(meta), 0o600); err != nil {
				t.Fatal(err)
			}

			res, err := PortCodexRollouts(context.Background(), home)
			if err != nil || len(res.Rewritten) != 1 || res.Rewritten[0] != path {
				t.Fatalf("pass: %+v, %v", res, err)
			}
			if after := lineEnds(t, path); !reflect.DeepEqual(after, before) {
				t.Fatalf("lines end at %v, were at %v", after, before)
			}
			got := readRollout(t, path)
			want, _ := json.Marshal(codexsummary.UserMessage(codexsummary.Prefix + "\n" + summary))
			var wantAny any
			json.Unmarshal(want, &wantAny)
			if p := decode(t, got[2])["payload"]; !reflect.DeepEqual(p, wantAny) {
				t.Errorf("not written portable: %s", got[2])
			}
			for i, l := range got {
				decode(t, l) // padded lines still read as JSON
				if i != 1 && (strings.Contains(l, codexsummary.Mark) || strings.Contains(l, claudeseal.Mark)) {
					t.Errorf("line %d still has magpie's item: %.200s", i, l)
				}
			}
			if b, _ := os.ReadFile(cpath); string(b) != meta {
				t.Error("the fork's rollout changed")
			}
		})
	}
}

// A line whose portable form is longer (a short summary, under Codex's
// prefix) can't grow in a paginated rollout, where Codex keeps each
// line's place by byte. It was left as it was, so the item went to OpenAI
// and the chat stopped on invalid_encrypted_content (hey_boyce on X): it
// goes as the summary under a short heading, as long as the line was,
// cut to fit when even that is longer (a summary of quotes, which JSON
// writes in two bytes and base64 in fewer).
func TestPortCodexRolloutsLongerLine(t *testing.T) {
	for _, c := range []struct{ name, summary, want string }{
		{"short", "short", "short"},
		{"cut", "keep this" + strings.Repeat(`"`, 3000) + "the end", shortHeading + "\nkeep this"},
	} {
		t.Run(c.name, func(t *testing.T) {
			lines := portRolloutLines(c.summary)
			lines[0] = strings.Replace(lines[0], `"model_provider":"openai"`, `"model_provider":"openai","history_mode":"paginated"`, 1)
			home, path := portHome(t, lines, false)
			before := lineEnds(t, path)
			if _, err := PortCodexRollouts(context.Background(), home); err != nil {
				t.Fatal(err)
			}
			if after := lineEnds(t, path); !reflect.DeepEqual(after, before) {
				t.Fatalf("lines end at %v, were at %v", after, before)
			}
			got := readRollout(t, path)
			for i, l := range got {
				if strings.Contains(l, `"encrypted_content":"magpie1:`) {
					t.Errorf("line %d still has magpie's item: %.300s", i, l)
				}
				if !json.Valid([]byte(l)) {
					t.Errorf("line %d isn't JSON: %.300s", i, l)
				}
			}
			for _, at := range []string{"payload.content.0.text", "payload.replacement_history.2.content.0.text"} {
				i := 2
				if strings.HasPrefix(at, "payload.replacement") {
					i = 3
				}
				text := gjson.Get(got[i], at).String()
				if !strings.HasPrefix(text, c.want) {
					t.Errorf("line %d's %s is %.200q, want it to begin %q", i, at, text, c.want)
				}
				if c.name == "cut" && (!strings.HasSuffix(text, "…") || strings.Contains(text, "the end")) {
					t.Errorf("line %d's summary isn't cut: %.200q…%.50q", i, text, text[max(0, len(text)-50):])
				}
			}
		})
	}
}

// An item with no id and a summary of a word has no message as short as
// it: it is retired as a type Codex never sends, never left to be sent.
func TestFitShorterRetires(t *testing.T) {
	line := []byte(`{"timestamp":"2026-10-20T07:40:02.551Z","type":"response_item","payload":{"type":"compaction","encrypted_content":"` + codexsummary.Encrypted("ok") + `"}}` + "\n")
	out, ok := fitShorter(line, false)
	if !ok || len(out) != len(line) {
		t.Fatalf("got %q, %v", out, ok)
	}
	if got := gjson.GetBytes(out, "payload.type").String(); got != retiredItem {
		t.Errorf("type %q, want %q: %s", got, retiredItem, out)
	}
	if gjson.GetBytes(out, "timestamp").String() != "2026-10-20T07:40:02.551Z" {
		t.Errorf("the line's head changed: %s", out)
	}
}

// A compacted line an earlier pass wrote with magpie_reasoning in its
// replacement_history (or a compaction item retired) had Codex send the
// item as "other", and OpenAI refused every turn after ("Invalid
// response.create payload: Invalid value: 'other'", real codex-cli
// 0.169.1). The next pass leaves it out, and its metadata entry with it,
// the line as long as it was; a magpie_reasoning response_item, which
// Codex never sends, stays.
func TestPortCodexRolloutsDropsRetiredFromHistory(t *testing.T) {
	lines := []string{
		`{"timestamp":"2026-10-30T07:05:20.118Z","type":"session_meta","payload":{"id":"` + portThread + `","model_provider":"openai","history_mode":"paginated"}}`,
		`{"timestamp":"2026-10-30T07:05:40.551Z","type":"response_item","payload":{"type":"magpie_reasoning","summary":[{"type":"summary_text","text":"先看 go.mod"}]}}`,
		`{"timestamp":"2026-10-30T07:40:02.553Z","type":"compacted","payload":{"message":"","replacement_history":[{"type":"message","role":"user","content":[{"type":"input_text","text":"修一下构建"}]},{"type":"magpie_reasoning","summary":[]},{"type":"magpie_compaction"},{"type":"message","role":"user","content":[{"type":"input_text","text":"go on"}]}],"replacement_history_metadata":[{"client_authored":true},{},{"compaction_model_hash":"h1"},{"client_authored":true}]}}`,
	}
	home, path := portHome(t, lines, false)
	ends := lineEnds(t, path)
	if _, err := PortCodexRollouts(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	got := readRollout(t, path)
	if !reflect.DeepEqual(lineEnds(t, path), ends) {
		t.Fatalf("lines moved: %v, were %v", lineEnds(t, path), ends)
	}
	if got[1] != lines[1] {
		t.Errorf("the response_item changed: %s", got[1])
	}
	p := decode(t, got[2])["payload"].(map[string]any)
	var types []string
	for _, it := range p["replacement_history"].([]any) {
		types = append(types, it.(map[string]any)["type"].(string))
	}
	if !reflect.DeepEqual(types, []string{"message", "message"}) {
		t.Errorf("replacement_history is %v: %s", types, got[2])
	}
	if m := p["replacement_history_metadata"].([]any); !reflect.DeepEqual(m, []any{map[string]any{"client_authored": true}, map[string]any{"client_authored": true}}) {
		t.Errorf("metadata %v", m)
	}
}

// A pass before this one looked at a paginated rollout, left its too-long
// line as it was, and never looked at it again, as no later pass reads a
// rollout last written before the one before it. One whole pass is made
// again, once.
func TestPortCodexRolloutsRefitsOnce(t *testing.T) {
	lines := portRolloutLines("short")
	lines[0] = strings.Replace(lines[0], `"model_provider":"openai"`, `"model_provider":"openai","history_mode":"paginated"`, 1)
	home, path := portHome(t, lines, false)
	st := codexPortState{Homes: map[string]codexPortHome{filepath.Clean(home): {Since: time.Now(), Lineage: true}}}
	b, _ := json.Marshal(st)
	if err := os.MkdirAll(filepath.Dir(codexPortPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codexPortPath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := PortCodexRollouts(context.Background(), home)
	if err != nil || len(res.Rewritten) != 1 {
		t.Fatalf("pass: %+v, %v", res, err)
	}
	if got := readRollout(t, path); strings.Contains(got[2], `"encrypted_content":"magpie1:`) {
		t.Errorf("not written: %s", got[2])
	}
	// the next pass reads only what changed since
	lines[2] = strings.Replace(lines[2], "cmp_magpie_1761000002", "cmp_magpie_1761000003", 1)
	b2 := []byte(strings.Join(lines, "\n") + "\n")
	os.WriteFile(path, b2, 0o640)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(path, old, old)
	if res, err := PortCodexRollouts(context.Background(), home); err != nil || len(res.Rewritten) != 0 {
		t.Errorf("second pass: %+v, %v", res, err)
	}
}

// A fork made before magpie kept its parent's lines whole names a byte
// the shorter parent doesn't end a line at, or doesn't have (xybio's
// thread). The pass after sets it again, once, from the ordinal the fork
// names, keeping the fork's first line as long as it was; a fork whose
// place is right keeps its bytes.
func TestPortCodexRolloutsRepairsForks(t *testing.T) {
	parent := []string{
		`{"timestamp":"2026-10-20T07:05:20.118Z","ordinal":0,"type":"session_meta","payload":{"id":"` + portThread + `","history_mode":"paginated","model_provider":"openai"}}`,
		`{"timestamp":"2026-10-20T07:05:31.002Z","ordinal":1,"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"fix it"}]}}`,
		`{"timestamp":"2026-10-20T07:05:40.002Z","ordinal":2,"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}}`,
	}
	home, path := portHome(t, parent, false)
	ends := lineEnds(t, path)
	dir := filepath.Dir(path)
	fork := func(id string, ord, at int) string {
		p := filepath.Join(dir, "rollout-2026-10-20T18-00-00-"+id+".jsonl")
		b := `{"timestamp":"2026-10-20T10:00:00.000Z","ordinal":` + strconv.Itoa(ord) + `,"type":"session_meta","payload":{"id":"` + id + `","history_mode":"paginated","history_base":{"thread_id":"` + portThread + `","end_ordinal_exclusive":` + strconv.Itoa(ord) + `,"end_byte_offset":` + strconv.Itoa(at) + `}}}` + "\n" +
			`{"timestamp":"2026-10-20T10:00:01.000Z","ordinal":` + strconv.Itoa(ord+1) + `,"type":"event_msg","payload":{"type":"task_started"}}` + "\n"
		if err := os.WriteFile(p, []byte(b), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// made when the parent was longer, at its end then
	broken := fork("01a1430d-e3a9-7a72-a466-071d2e3dd46b", 3, 14362)
	right := fork("01a1430d-ffff-7a72-a466-071d2e3dd46b", 2, ends[1])
	was, _ := os.ReadFile(broken)
	rightWas, _ := os.ReadFile(right)

	res, err := PortCodexRollouts(context.Background(), home)
	if err != nil || len(res.Repaired) != 1 || res.Repaired[0] != broken {
		t.Fatalf("pass: %+v, %v", res, err)
	}
	now, _ := os.ReadFile(broken)
	if len(now) != len(was) {
		t.Fatalf("%d bytes, was %d", len(now), len(was))
	}
	first := decode(t, readRollout(t, broken)[0])
	if at := first["payload"].(map[string]any)["history_base"].(map[string]any)["end_byte_offset"]; at != float64(ends[2]) {
		t.Errorf("end_byte_offset %v, want %d", at, ends[2])
	}
	if b, _ := os.ReadFile(right); !bytes.Equal(b, rightWas) {
		t.Error("a fork whose place was right changed")
	}
	// looked at once
	os.WriteFile(broken, was, 0o600)
	if res, err := PortCodexRollouts(context.Background(), home); err != nil || len(res.Repaired) != 0 {
		t.Fatalf("second pass: %+v, %v", res, err)
	}
}

// hentulong (X): Codex routed through magpie to WorkBuddy, then back to
// its own ChatGPT sign-in, couldn't go on with the thread or a fork of it:
//
//	{"type":"error","error":{"message":"Persisted response contains hosted-tool, compaction, or unverifiable hidden reasoning state that Rustponses cannot replay. Start a new response or use the Python Responses service for this continuation.","type":"invalid_request_error","param":"previous_response_id","code":"unsupported_persisted_item_context"},"status":400}
//
// The thread holds the reasoning items magpie gave Codex for WorkBuddy's
// translated replies: a summary, magpie's id, encrypted_content null, as
// Codex writes them. Codex's websocket sends the thread as a warmup and
// the turn with previous_response_id; OpenAI takes the warmup and refuses
// the turn with that error (tried on a real ChatGPT account, gpt-6-sol;
// the same history without the item goes on). Line 6 is the one a real
// Codex 0.165 wrote for auto/deepseek-flash through magpie, which, on
// Codex's own sign-in after, was refused with array_above_max_length
// ("Invalid 'input[5].content': array too long"). A pass made as magpie
// lets Codex go writes each reasoning item OpenAI didn't seal as
// magpie_reasoning, a type Codex reads as Other and never sends, in a
// response_item; in replacement_history, where Codex sends an Other as
// "other" and OpenAI refuses the turn ("Invalid value: 'other'", real
// codex-cli 0.165.0 and 0.169.1), it is left out with its metadata entry.
// Each line keeps its length; OpenAI's own sealed reasoning and every
// other line keep their bytes. A pass while Codex goes through magpie
// leaves them: there a DeepSeek or Kimi model gets its reasoning back
// from them. A thread begun on a provider of the user's own is left be.
func TestPortCodexRolloutsLeftRetiresUnsealedReasoning(t *testing.T) {
	sealed := claudeseal.Seal([]byte(`{"type":"thinking","thinking":"hm","signature":"sig"}`))
	lines := []string{
		`{"timestamp":"2026-10-30T07:05:20.118Z","type":"session_meta","payload":{"id":"` + portThread + `","timestamp":"2026-10-30T07:05:20.101Z","cwd":"/Users/me/w","originator":"codex_cli_rs","cli_version":"0.170.0","source":"cli","model_provider":"openai","history_mode":"paginated"}}`,
		`{"timestamp":"2026-10-30T07:05:31.002Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"修一下构建 \"type\":\"reasoning\""}]}}`,
		`{"timestamp":"2026-10-30T07:05:40.551Z","type":"response_item","payload":{"type":"reasoning","id":"rs_18b3f2a9c1d2e3f40000002a","summary":[{"type":"summary_text","text":"先看 go.mod，再跑测试。"}],"encrypted_content":null}}`,
		`{"timestamp":"2026-10-30T07:05:41.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"好了"}]}}`,
		`{"timestamp":"2026-10-30T07:40:02.553Z","type":"compacted","payload":{"message":"","replacement_history":[{"type":"message","role":"user","content":[{"type":"input_text","text":"修一下构建"}]},{"type":"reasoning","id":"rs_18b3f2a9c1d2e3f40000002b","summary":[],"encrypted_content":null},{"type":"reasoning","id":"rs_0a1b2c3d4e5f60718293a4b5c6d7e8f9","summary":[],"encrypted_content":"gAAAAABo_OpenAIsOwn=="}],"replacement_history_metadata":[{"client_authored":true},{},{}]}}`,
		`{"timestamp":"2026-10-30T08:00:00.000Z","type":"response_item","payload":{"type":"reasoning","id":"rs_0a1b2c3d4e5f60718293a4b5c6d7e8f9","summary":[{"type":"summary_text","text":"x"}],"encrypted_content":"gAAAAABo_OpenAIsOwn=="}}`,
		`{"timestamp":"2026-10-31T09:26:38.975Z","ordinal":12,"type":"response_item","payload":{"type":"reasoning","id":"f353ba26-5235-430b-87ee-76a8ad936ef2","summary":[],"content":[{"type":"reasoning_text","text":"We need answer just number. 17*23=391."}],"encrypted_content":"f1ec9b47-8fb3-4f29-8115-90aa9bb862b0-0","internal_chat_message_metadata_passthrough":{"turn_id":"01a19148-3e68-7bc2-82be-0c4681743dab"}}}`,
		`{"timestamp":"2026-10-30T08:00:02.000Z","type":"response_item","payload":{"type":"reasoning","id":"rs_18b3f2a9c1d2e3f40000002c","summary":[],"encrypted_content":"` + sealed + `"}}`,
	}
	for _, zst := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "zst"}[zst], func(t *testing.T) {
			home, path := portHome(t, lines, zst)
			ends := lineEnds(t, path)

			// through magpie: magpie's unsealed reasoning stays; Claude's
			// block goes as before
			if _, err := PortCodexRollouts(context.Background(), home); err != nil {
				t.Fatal(err)
			}
			got := readRollout(t, path)
			if got[2] != lines[2] || got[4] != lines[4] {
				t.Fatalf("written while Codex goes through magpie:\n%s\n%s", got[2], got[4])
			}

			res, err := PortCodexRolloutsLeft(context.Background(), home)
			if err != nil || !res.Done || len(res.Rewritten) != 1 {
				t.Fatalf("left pass: %+v, %v", res, err)
			}
			got = readRollout(t, path)
			if len(got) != len(lines) || !reflect.DeepEqual(lineEnds(t, path), ends) {
				t.Fatalf("lines moved: %v, were %v", lineEnds(t, path), ends)
			}
			for _, i := range []int{0, 1, 3, 5} {
				if got[i] != lines[i] {
					t.Errorf("line %d changed:\n%s\nwas\n%s", i, got[i], lines[i])
				}
			}
			p2 := decode(t, got[2])["payload"].(map[string]any)
			if p2["type"] != "magpie_reasoning" || p2["encrypted_content"] != nil || p2["id"] != nil ||
				!reflect.DeepEqual(p2["summary"], []any{map[string]any{"type": "summary_text", "text": "先看 go.mod，再跑测试。"}}) {
				t.Errorf("magpie's reasoning item: %s", got[2])
			}
			p4 := decode(t, got[4])["payload"].(map[string]any)
			hist := p4["replacement_history"].([]any)
			if len(hist) != 2 || hist[0].(map[string]any)["type"] != "message" ||
				hist[1].(map[string]any)["type"] != "reasoning" || hist[1].(map[string]any)["encrypted_content"] != "gAAAAABo_OpenAIsOwn==" {
				t.Errorf("replacement_history %v", hist)
			}
			if m := p4["replacement_history_metadata"].([]any); len(m) != 2 || !reflect.DeepEqual(m[0], map[string]any{"client_authored": true}) {
				t.Errorf("metadata %v", m)
			}
			if p7 := decode(t, got[7])["payload"].(map[string]any); p7["type"] != "magpie_reasoning" || strings.Contains(got[7], claudeseal.Mark) {
				t.Errorf("Claude's block: %s", got[7])
			}
			if !strings.HasPrefix(got[6], `{"timestamp":"2026-10-31T09:26:38.975Z","ordinal":12,"type":"response_item","payload":{"type":"magpie_reasoning","summary":[],"content":[{"type":"reasoning_text","text":"We need answer just number. 17*23=391."}],"internal_chat_message_metadata_passthrough":{"turn_id":"01a19148-3e68-7bc2-82be-0c4681743dab"}}}  `) {
				t.Errorf("another API's reasoning: %s", got[6])
			}
			// nothing OpenAI can't take as a base is left
			for i, l := range got {
				if i != 1 && strings.Contains(l, `"type":"reasoning"`) && !strings.Contains(l, "gAAAA") {
					t.Errorf("line %d still has magpie's reasoning: %s", i, l)
				}
			}
			// magpie still reads it as the model's output
			if !cxAnswerItem([]byte(got[2])) {
				t.Error("cxAnswerItem doesn't take magpie_reasoning")
			}
			if res, err := PortCodexRolloutsLeft(context.Background(), home); err != nil || len(res.Rewritten) != 0 {
				t.Errorf("second left pass: %+v, %v", res, err)
			}
		})
	}
	t.Run("own provider", func(t *testing.T) {
		own := append([]string{strings.Replace(lines[0], `"model_provider":"openai"`, `"model_provider":"openrouter"`, 1)}, lines[1:7]...)
		home, path := portHome(t, own, false)
		orig, _ := os.ReadFile(path)
		if res, err := PortCodexRolloutsLeft(context.Background(), home); err != nil || len(res.Rewritten) != 0 {
			t.Fatalf("left pass: %+v, %v", res, err)
		}
		if b, _ := os.ReadFile(path); !bytes.Equal(b, orig) {
			t.Fatal("a thread on the user's own provider was written")
		}
	})
}

// A pass through magpie that ran first doesn't keep a left pass from
// looking at what it looked at: each keeps its own state.
func TestPortCodexRolloutsLeftKeepsItsOwnState(t *testing.T) {
	line := `{"timestamp":"2026-10-30T07:05:40.551Z","type":"response_item","payload":{"type":"reasoning","id":"rs_18b3f2a9c1d2e3f40000002a","summary":[],"encrypted_content":null}}`
	home, path := portHome(t, []string{line}, false)
	for i := 0; i < 2; i++ {
		if _, err := PortCodexRollouts(context.Background(), home); err != nil {
			t.Fatal(err)
		}
	}
	if res, err := PortCodexRolloutsLeft(context.Background(), home); err != nil || len(res.Rewritten) != 1 {
		t.Fatalf("left pass after two others: %+v, %v", res, err)
	}
	if got := readRollout(t, path); !strings.Contains(got[0], `"magpie_reasoning"`) {
		t.Fatalf("not written: %s", got[0])
	}
}

// mustPort is the port's rewrite of a line, which the fixtures here are
// built from: what the first port wrote is what it still writes, unpadded.
func mustPort(line string) string {
	out, ok := portLine([]byte(line))
	if !ok {
		panic("fixture: the port doesn't take the line")
	}
	return string(out)
}

// unportOriginal is a Codex 0.164 rollout of a thread magpie compacted,
// as Codex writes it: a paginated session_meta, a user message and the
// compacted checkpoint holding magpie's item last in its
// replacement_history, an ordinal on every line.
func unportOriginal(summary string) []string {
	enc := codexsummary.Encrypted(summary)
	return []string{
		`{"timestamp":"2026-10-20T07:05:20.118Z","type":"session_meta","ordinal":1,"payload":{"id":"` + portThread + `","timestamp":"2026-10-20T07:05:20.101Z","cwd":"/Users/me/w","originator":"codex_cli_rs","cli_version":"0.164.0","source":"cli","model_provider":"openai","history_mode":"paginated"}}`,
		`{"timestamp":"2026-10-20T07:06:00.000Z","type":"response_item","ordinal":2,"payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the build"}]}}`,
		`{"timestamp":"2026-10-20T07:40:02.553Z","type":"compacted","ordinal":3,"payload":{"message":"","replacement_history":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions instructions>"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the build"}]},{"type":"compaction","id":"cmp_magpie_1761000002","encrypted_content":"` + enc + `"}],"replacement_history_metadata":[{"client_authored":true},{},{"compaction_model_hash":"h1"}],"window_number":2,"compaction_response_id":"resp_magpie_1761000002","latest_token_usage_record":{"input_tokens":182734,"output_tokens":1204}}}`,
	}
}

// writeFork writes a fork's rollout of the thread, whose session_meta
// names where the parent's history ends: at line 3's end, as Codex
// recorded it when the parent was whole.
func writeFork(t *testing.T, dir, parent string, at int) string {
	t.Helper()
	meta := `{"timestamp":"2026-10-20T16:00:00.000Z","type":"session_meta","payload":{"id":"0f0e0d0c-0b0a-0908-0700-665554433221","timestamp":"2026-10-20T16:00:00.000Z","cwd":"/Users/me/w","originator":"codex_cli_rs","cli_version":"0.164.0","source":"cli","model_provider":"openai","history_mode":"paginated","history_base":{"thread_id":"` + parent + `","end_byte_offset":` + strconv.Itoa(at) + `,"end_ordinal_exclusive":4}}}`
	p := filepath.Join(dir, "rollout-2026-10-20T16-00-00-0f0e0d0c-0b0a-0908-0700-665554433221.jsonl")
	if err := os.WriteFile(p, []byte(meta+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	return p
}

// The lines the first port (4896fc3ba, before 67b12956a) wrote at a wrong
// length go back at the length they had: the item the compacted line's
// message was written for is measured from the line itself (the summary
// in the message, the id from the line's compaction_response_id), so a
// line the message left shorter is padded with spaces after its JSON and
// one it left longer has the item written back, and every byte place
// Codex keeps past it names a line end again — a fork's history_base,
// which repairLineages set where the parent ended short, is set again to
// where the parent ends at its length. A second pass has nothing to do.
func TestUnportCodexRollouts(t *testing.T) {
	summary := strings.Repeat("Progress on the build. ", 90) // the item's seal is base64, longer than the message
	orig := unportOriginal(summary)
	damaged, _ := portLine([]byte(orig[2]))
	if bytes.Contains(damaged, []byte(codexsummary.Mark)) || !bytes.Contains(damaged, []byte(codexsummary.Prefix[:20])) {
		t.Fatalf("the first port's line is %s", damaged)
	}
	whole := len(orig[0]) + len(orig[1]) + len(orig[2]) + 3
	short := len(orig[0]) + len(orig[1]) + len(damaged) + 3
	if short >= whole {
		t.Fatalf("fixture: the ported line is not shorter: %d vs %d", short, whole)
	}

	for _, zst := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "zst"}[zst], func(t *testing.T) {
			home, path := portHome(t, []string{orig[0], orig[1], string(damaged)}, zst)
			fork := writeFork(t, filepath.Dir(path), portThread, short)

			res, err := PortCodexRollouts(context.Background(), home)
			if err != nil || !res.Done || len(res.Repaired) != 1 {
				t.Fatalf("pass: %+v, %v", res, err)
			}
			got := readRollout(t, path)
			if len(got) != 3 || got[0] != orig[0] || got[1] != orig[1] {
				t.Fatalf("lines: %d, first two kept: %v", len(got), len(got) == 3 && got[0] == orig[0] && got[1] == orig[1])
			}
			if len(got[2]) != len(orig[2]) {
				t.Fatalf("line 3 is %d bytes, the length it had is %d", len(got[2]), len(orig[2]))
			}
			if !bytes.HasPrefix([]byte(got[2]), damaged) || !strings.HasSuffix(got[2], strings.Repeat(" ", len(orig[2])-len(damaged))) {
				t.Errorf("line 3 is not the port's line padded: %.120s", got[2])
			}
			p := decode(t, got[2])["payload"].(map[string]any)
			hist := p["replacement_history"].([]any)
			want, _ := json.Marshal(codexsummary.UserMessage(codexsummary.Prefix + "\n" + summary))
			var wantAny any
			json.Unmarshal(want, &wantAny)
			if len(hist) != 3 || !reflect.DeepEqual(hist[2], wantAny) || p["compaction_response_id"] != "resp_magpie_1761000002" {
				t.Errorf("replacement_history %v", hist)
			}
			if b, _ := os.ReadFile(path); !zst && len(b) != whole {
				t.Errorf("rollout is %d bytes, it was %d", len(b), whole)
			}
			if n := len(got[0]) + len(got[1]) + len(got[2]) + 3; n != whole {
				t.Errorf("rollout's lines are %d bytes, they were %d", n, whole)
			}
			// the fork's place is where the parent's line 3 ends at its
			// length again, which is what Codex recorded when it made it
			fm := decode(t, readRollout(t, fork)[0])["payload"].(map[string]any)["history_base"].(map[string]any)
			if fm["end_byte_offset"] != float64(whole) {
				t.Errorf("fork's end_byte_offset is %v, want %d", fm["end_byte_offset"], whole)
			}
			// the pass is remembered, so the lines are not looked at again
			var st struct {
				Homes map[string]struct {
					Unport bool `json:"unport"`
				} `json:"homes"`
			}
			b, err := os.ReadFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "magpie", "codex-portable.json"))
			if err != nil || json.Unmarshal(b, &st) != nil || !st.Homes[filepath.Clean(home)].Unport {
				t.Errorf("the pass is not in the state: %s, %v", b, err)
			}
			// a second pass has nothing to do
			before, _ := os.ReadFile(path)
			if res, err := PortCodexRollouts(context.Background(), home); err != nil || len(res.Rewritten) != 0 || len(res.Repaired) != 0 {
				t.Errorf("second pass: %+v, %v", res, err)
			}
			if after, _ := os.ReadFile(path); !bytes.Equal(after, before) {
				t.Error("the second pass wrote the rollout")
			}
		})
	}
}

// A line the first port left longer (a short summary: the message is
// Codex's long prefix and the item's seal) has the item written back, and
// that is ported again in the same pass, as the port now writes a line
// too long for a paginated rollout: the summary under Codex's own short
// heading, padded to the length the line had.
func TestUnportCodexRolloutsPutsTheItemBack(t *testing.T) {
	summary := strings.Repeat("ok. ", 25) // the item's seal is shorter than the message, and the summary fits under the short heading
	orig := unportOriginal(summary)
	damaged, _ := portLine([]byte(orig[2]))
	if len(damaged) <= len(orig[2]) {
		t.Fatalf("fixture: the ported line is not longer: %d vs %d", len(damaged), len(orig[2]))
	}
	home, path := portHome(t, []string{orig[0], orig[1], string(damaged)}, false)
	res, err := PortCodexRollouts(context.Background(), home)
	if err != nil || !res.Done || len(res.Rewritten) != 1 {
		t.Fatalf("pass: %+v, %v", res, err)
	}
	got := readRollout(t, path)
	if len(got[2]) != len(orig[2]) {
		t.Fatalf("line 3 is %d bytes, the length it had is %d", len(got[2]), len(orig[2]))
	}
	if strings.Contains(got[2], codexsummary.Mark) {
		t.Errorf("line 3 holds magpie's item: %s", got[2])
	}
	p := decode(t, got[2])["payload"].(map[string]any)
	hist := p["replacement_history"].([]any)
	want, _ := json.Marshal(codexsummary.UserMessage(shortHeading + "\n" + summary))
	var wantAny any
	json.Unmarshal(want, &wantAny)
	if len(hist) != 3 || !reflect.DeepEqual(hist[2], wantAny) {
		t.Errorf("replacement_history %v", hist)
	}
}

// A rollout with a line that can't be measured is left as it is: a ported
// item alone in its line (Codex 0.162 kept it there) has its id nowhere,
// a reasoning item the first port stripped its sealed content from is not
// kept anywhere either, two of magpie's in one compacted line name one id
// between them, and a rollout Codex keeps no byte places in (not
// paginated) reads the same at any length.
func TestUnportCodexRolloutsLeavesWhatItCantMeasure(t *testing.T) {
	summary := "Progress: done."
	enc := codexsummary.Encrypted(summary)
	alone := `{"timestamp":"2026-10-20T07:40:02.551Z","type":"response_item","ordinal":3,"payload":{"type":"compaction","id":"cmp_magpie_1761000002","encrypted_content":"` + enc + `"},"metadata":{"compaction_model_hash":"h1"}}`
	sealed := claudeseal.Seal([]byte(`{"type":"thinking","thinking":"hm","signature":"sig"}`))
	stripped := `{"timestamp":"2026-10-20T07:40:02.553Z","type":"compacted","ordinal":3,"payload":{"message":"","replacement_history":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions instructions>"}]},{"type":"reasoning","summary":[],"encrypted_content":"` + sealed + `"},{"type":"compaction","id":"cmp_magpie_1761000002","encrypted_content":"` + enc + `"}],"replacement_history_metadata":[{"client_authored":true},{},{"compaction_model_hash":"h1"}],"window_number":2,"compaction_response_id":"resp_magpie_1761000002"}}`
	two := `{"timestamp":"2026-10-20T07:40:02.553Z","type":"compacted","ordinal":3,"payload":{"message":"","replacement_history":[{"type":"compaction","id":"cmp_magpie_1761000001","encrypted_content":"` + enc + `"},{"type":"compaction","id":"cmp_magpie_1761000002","encrypted_content":"` + enc + `"}],"replacement_history_metadata":[{},{}],"compaction_response_id":"resp_magpie_1761000002"}}`
	for _, tc := range []struct {
		name string
		orig []string
	}{
		{"item alone in its line", []string{unportOriginal(summary)[0], unportOriginal(summary)[1], alone}},
		{"Claude's block stripped", []string{unportOriginal(summary)[0], unportOriginal(summary)[1], stripped}},
		{"two of magpie's in one line", []string{unportOriginal(summary)[0], unportOriginal(summary)[1], two}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var lines []string
			for i, l := range tc.orig {
				if i < 2 {
					lines = append(lines, l)
					continue
				}
				lines = append(lines, string(mustPort(l)))
			}
			home, path := portHome(t, lines, false)
			before, _ := os.ReadFile(path)
			if res, err := PortCodexRollouts(context.Background(), home); err != nil || len(res.Rewritten) != 0 {
				t.Fatalf("pass: %+v, %v", res, err)
			}
			if after, _ := os.ReadFile(path); !bytes.Equal(after, before) {
				t.Error("the rollout was written")
			}
		})
	}
	t.Run("not paginated", func(t *testing.T) {
		orig := unportOriginal(summary)
		legacy := strings.Replace(orig[0], `"history_mode":"paginated"`, `"history_mode":"legacy"`, 1)
		home, path := portHome(t, []string{legacy, orig[1], mustPort(orig[2])}, false)
		before, _ := os.ReadFile(path)
		if res, err := PortCodexRollouts(context.Background(), home); err != nil || len(res.Rewritten) != 0 {
			t.Fatalf("pass: %+v, %v", res, err)
		}
		if after, _ := os.ReadFile(path); !bytes.Equal(after, before) {
			t.Error("the rollout was written")
		}
	})
}

// A line the port wrote at the length it had (fitLine pads it) is left as
// it is, whether or not anything else in the rollout is due.
func TestUnportCodexRolloutsLeavesThePortsOwn(t *testing.T) {
	summary := strings.Repeat("Progress on the build. ", 90)
	orig := unportOriginal(summary)
	damaged, _ := portLine([]byte(orig[2]))
	padded := string(damaged) + strings.Repeat(" ", len(orig[2])-len(damaged))
	home, path := portHome(t, []string{orig[0], orig[1], padded}, false)
	before, _ := os.ReadFile(path)
	if res, err := PortCodexRollouts(context.Background(), home); err != nil || len(res.Rewritten) != 0 {
		t.Fatalf("pass: %+v, %v", res, err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(after, before) {
		t.Error("the rollout was written")
	}
}
