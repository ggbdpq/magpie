package claudetui

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"
)

// TestWholeIn: the words Claude Code records of a pasted message are taken
// as whole whatever it trims, wraps, numbers or takes out, and a message
// that lost a block of its words is not (#5205, which lost 1 KB blocks),
// nor one that lost an image.
func TestWholeIn(t *testing.T) {
	long := strings.Repeat("line of words 中文 ", 400)
	cases := []struct {
		name   string
		sent   string
		said   []string
		whole  bool
		images int        // sent
		rows   []heardRow // said, with its images
	}{
		{"as sent", "hello there", []string{"hello there"}, true, 0, nil},
		{"trimmed and wrapped", "\n/command and more\n", []string{"/command and more"}, true, 0, nil},
		{"a long paste's tags", long, []string{"\n<pasted_content id=\"a1\">\n" + long + "\n</pasted_content id=\"a1\">\n"}, true, 0, nil},
		{"image marks", "look at this\n\n", []string{"[Image #1] look at this"}, true, 0, nil},
		{"a zero-width space taken out", "a\u200bb", []string{"ab"}, true, 0, nil},
		{"tabs as spaces", "a\tb", []string{"a    b"}, true, 0, nil},
		{"the user's mark", userMark + "go on", []string{"[user message]\ngo on"}, true, 0, nil},
		{"a later row is its", "the message", []string{"another row", "the message"}, true, 0, nil},
		{"no row read", "the message", nil, true, 0, nil},
		{"a block lost", long, []string{long[:1024] + long[2048:]}, false, 0, nil},
		{"its end lost", long, []string{long[:len(long)/2]}, false, 0, nil},
		{"taken as many pastes", long, []string{"<pasted_content id=\"a1\">\n" + long[:990] + "\n</pasted_content id=\"a1\">\n<pasted_content id=\"a2\">\n" + long[990:] + "\n</pasted_content id=\"a2\">"}, false, 0, nil},
		{"other words", "the message", []string{"another row"}, false, 0, nil},
		{"its images", "look\n", nil, true, 2, []heardRow{{"[Image #1] [Image #2] look", 2}}},
		{"an image lost", "look\n", nil, false, 2, []heardRow{{"[Image #1] look", 1}}},
		{"an image alone", "", nil, true, 1, []heardRow{{"[Image #1]", 1}}},
		{"a queued row says no images", "look\n", nil, true, 1, []heardRow{{"look", -1}}},
		{"images, but a word lost", "look here\n", nil, false, 1, []heardRow{{"look", 1}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows := c.rows
			for _, w := range c.said {
				rows = append(rows, heardRow{w, 0})
			}
			err := wholeIn(c.sent, c.images, 1, rows, nil)
			if (err == nil) != c.whole {
				t.Fatalf("wholeIn = %v, want whole %v", err, c.whole)
			}
			var cut *messageCut
			if err != nil && !errors.As(err, &cut) {
				t.Fatalf("wholeIn = %#v, want a messageCut", err)
			}
		})
	}
}

// BenchmarkWholeIn: a 256 KB message checked against its record, a long
// paste's, read in place.
func BenchmarkWholeIn(b *testing.B) {
	sent := strings.Repeat("line of words 中文 and more\n", 256<<10/30)
	said := []heardRow{{"\n<pasted_content id=\"a1\">\n" + sent + "</pasted_content id=\"a1\">\n", 0}}
	b.SetBytes(int64(len(sent)))
	b.ReportAllocs()
	for b.Loop() {
		if wholeIn(sent, 0, 1, said, nil) != nil {
			b.Fatal("not whole")
		}
	}
}

// TestWordsSkipRange: none of U+3001 to U+FDFF that words reads without
// asking unicode is white space or unseen, and those it leaves out are
// unseen.
func TestWordsSkipRange(t *testing.T) {
	for r := rune(0x3001); r < 0xfe00; r++ {
		if r != 0x3164 && (unicode.IsSpace(r) || unseen(r)) {
			t.Fatalf("%U is white space or unseen", r)
		}
	}
	for _, r := range []rune{0x3164, 0xfe00, 0xfe0f} {
		if !unseen(r) {
			t.Fatalf("%U is seen", r)
		}
	}
}

// TestWholeInUnseen: characters that show nothing aren't compared, taken
// out or left in; a letter lost beside one still is.
func TestWholeInUnseen(t *testing.T) {
	for _, c := range []struct {
		name       string
		sent, said string
		whole      bool
	}{
		{"an ideographic variation selector", "葛\U000E0100城 here", "葛城 here", true},
		{"a variation selector alone", "a\ufe0fb", "ab", true},
		{"an emoji's variation selector, kept", "a \u2764\ufe0f b", "a \u2764\ufe0f b", true},
		{"Hangul fillers", "a\u3164b a\u115fb a\uffa0b", "ab ab ab", true},
		{"a grapheme joiner", "a\u034fb", "ab", true},
		{"a C1 control", "a\u0080b", "ab", true},
		{"zero-width and bidi characters", "a\u200b\u200d\u2060\u00ad\ufeff\u202eb", "ab", true},
		{"a letter lost beside one", "a\u3164b", "a", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := wholeIn(c.sent, 0, 1, []heardRow{{c.said, 0}}, nil)
			if (err == nil) != c.whole {
				t.Fatalf("wholeIn = %v, want whole %v", err, c.whole)
			}
		})
	}
}

// TestWholeInPastes: a row whose own tags open more pastes than the
// message's pieces isn't whole.
func TestWholeInPastes(t *testing.T) {
	long := strings.Repeat("line of words 中文 ", 400)
	for _, c := range []struct {
		name   string
		sent   string
		pieces int
		row    string
		whole  bool
		says   string
	}{
		{"split in a word", long, 1, wrap("a1", long[:990]) + wrap("a2", long[990:]), false, "as 2 pastes, of 1"},
		{"split between words", "one two three four", 1, wrap("a1", "one two") + wrap("a2", "three four"), false, "as 2 pastes, of 1"},
		{"two pieces, two tags", "one two\nthree four\n", 2, wrap("a1", "one two") + wrap("a2", "three four"), true, ""},
		{"one piece, one tag", long, 1, wrap("a1", long), true, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := wholeIn(c.sent, 0, c.pieces, []heardRow{{c.row, 0}}, nil)
			if (err == nil) != c.whole {
				t.Fatalf("wholeIn = %v, want whole %v", err, c.whole)
			}
			if err != nil && !strings.Contains(err.Error(), c.says) {
				t.Fatalf("wholeIn = %v, want %q", err, c.says)
			}
		})
	}
}

// wrap is s in a paste tag of Claude Code's own.
func wrap(id, s string) string {
	return "\n\n<pasted_content id=\"" + id + "\">\n" + s + "\n</pasted_content id=\"" + id + "\">\n"
}

// TestMessageCutSaysWhere: a message recorded otherwise is told by where
// it and its record first differ, with none of its words, and each error
// fits the bridge's 200-character log line after Unpastable, with Windows
// 10's cutHint.
func TestMessageCutSaysWhere(t *testing.T) {
	err := wholeIn("a secret word here", 0, 1, []heardRow{{"a secret ward here", 0}}, nil)
	if err == nil || !strings.HasPrefix(err.Error(), "U+006F at byte 10 was recorded as U+0061 at 10") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("wholeIn = %v, want where, with no words", err)
	}
	err = wholeIn("all of it", 0, 1, []heardRow{{"all of it and more", 0}}, nil)
	if err == nil || !strings.HasPrefix(err.Error(), "the end at byte 9 was recorded as U+0061 at 10") {
		t.Fatalf("wholeIn = %v, want the end named", err)
	}
	const hint = len("; on Windows 10, a conpty folder beside magpie.exe fixes it (#5205)")
	n := 9999999
	for _, cut := range []*messageCut{
		{gotImages: n - 1, sentImages: n},
		{pastes: n, pieces: n - 1},
		{got: n - 1, sent: n},
		{at: differs{n, n, 0x10fffd, -1}},
	} {
		said := Unpastable + strings.TrimSuffix(cut.Error(), cutHint())
		if len(said)+hint > 200 || strings.Contains(said, ImageNotTaken) {
			t.Errorf("%d characters with the hint: %s", len(said)+hint, said)
		}
	}
}

// TestWholeInRecorded: records of real runs of Claude Code 2.1.319 on
// Windows 10, through the system's ConPTY and magpie's own
// (testdata/whole-*-2.1.319.json), are judged as they were in truth.
func TestWholeInRecorded(t *testing.T) {
	for _, c := range []struct {
		name  string
		whole bool
	}{
		{"tagdots-system", true},
		{"tagid-system", true},
		{"inl-system", false},
		{"ml-system", false},
		{"c1-system", false},
		{"queuetag-system", true},
		{"queue-system", true},
		{"mixed-system", true},
		{"short2-system", true},
		{"pathlines-system", true},
		{"longpaths-system", false},
		{"long64-system", false},
		{"inl-own", true},
		{"ml-own", true},
		{"c1-own", true},
		{"queuetag-own", true},
		{"longpaths-own", true},
		{"pathlines-own", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			sent, images, pieces, rows, taken := recorded(t, "whole-"+c.name+"-2.1.319.json")
			err := wholeIn(sent, images, pieces, rows, taken)
			if (err == nil) != c.whole {
				t.Fatalf("wholeIn = %v, want whole %v", err, c.whole)
			}
		})
	}
}

// recorded reads a record of a run: the messages sent to the helper, and
// the rows Claude Code's transcript holds after them. The last message is
// pasted as send pastes it, and its rows are the ones after the first
// message's.
func recorded(t *testing.T, file string) (sent string, images, pieces int, rows []heardRow, taken []string) {
	b, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Sent []json.RawMessage `json:"sent"`
		Rows []struct {
			Type      string          `json:"type"`
			IsMeta    bool            `json:"isMeta"`
			Operation string          `json:"operation"`
			Content   string          `json:"content"`
			Message   json.RawMessage `json:"message"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	var text string
	var blocks []struct{ Type, Text string }
	if json.Unmarshal(d.Sent[len(d.Sent)-1], &text) == nil {
		blocks = append(blocks, struct{ Type, Text string }{"text", text})
	} else if err := json.Unmarshal(d.Sent[len(d.Sent)-1], &blocks); err != nil {
		t.Fatal(err)
	}
	for _, bl := range blocks {
		if bl.Type == "image" {
			images++
			continue
		}
		if pieces == 0 {
			sent = pasteable(bl.Text, len(d.Sent) > 1) + "\n"
		} else {
			sent += pasteClean(bl.Text) + "\n"
		}
		pieces++
	}
	first := len(d.Sent) > 1
	for _, r := range d.Rows {
		switch {
		case r.Type == "queue-operation" && r.Operation == "enqueue":
			rows = append(rows, heardRow{r.Content, -1})
		case r.Type == "user" && r.IsMeta:
			taken = append(taken, imageSources(r.Message)...)
		case r.Type == "user" && first:
			first = false
		case r.Type == "user":
			rows = append(rows, promptRow(r.Message))
		}
	}
	return sent, images, pieces, rows, taken
}

// TestWholeInTally: messages whole or not in truth, as Claude Code or the
// console rewrote them (seen) or might (made up), each with whether the
// check is known to judge it wrong.
func TestWholeInTally(t *testing.T) {
	long := strings.Repeat("line of words 中文 ", 400)
	quoting := "Do NOT delete prod. The tags are <pasted_content id=\"ab12\"> and </pasted_content id=\"ab12\"> and <pasted_content>. Go on."
	rule := strings.Repeat(strings.Repeat("=", 78)+"\n", 14)
	screen := strings.Repeat("\u2502"+strings.Repeat(" ", 138)+"\u2502\n", 4) + strings.Repeat("\u2500", 140) + "\n"
	told, missed := 0, 0
	for _, c := range []struct {
		kind   string // seen or made up
		name   string
		sent   string
		pieces int
		row    string
		whole  bool // in truth
		wrong  bool // the check says otherwise
	}{
		{"seen", "its tag quoted, escaped", quoting, 1, escaped(quoting), true, false},
		{"seen", "its tag quoted, escaped and wrapped", quoting, 1, wrap("4302", escaped(quoting)), true, false},
		{"seen", "every tag 2.1.319 escapes", tagsQuoted, 1, wrap("1584", tagsEscaped), true, false},
		{"seen", "an ideographic variation selector taken out", "葛\U000E0100城 here", 1, "葛城 here", true, false},
		{"seen", "a stray variation selector taken out", "a\ufe0fb \u2764\ufe0f", 1, "ab \u2764\ufe0f", true, false},
		{"seen", "a Hangul filler taken out", "a\u3164b a\u115fb a\uffa0b", 1, "ab ab ab", true, false},
		{"seen", "a joiner taken out", "a\u034fb", 1, "ab", true, false},
		{"seen", "a C1 control taken out", "a\u0080b", 1, "ab", true, false},
		{"seen", "an image mark of the message's own", "see [Image #1] in whole.go", 1, "see [Image #1] in whole.go", true, false},
		{"seen", "an image mark of its own after Claude Code's", "see [Image #1] here", 1, "[Image #1] see [Image #1] here", true, false},
		{"seen", "two pastes, two tags", "one two\nthree four\n", 2, wrap("a1", "one two") + wrap("a2", "three four"), true, false},
		{"seen", "a fullwidth < before its tag", "a \uff1cpasted_content b", 1, "a <" + `\` + "pasted_content b", true, false},
		{"made", "its tag quoted, not escaped (an older Claude Code)", "a <pasted_content id=\"ab12\"> b", 1, "a <pasted_content id=\"ab12\"> b", true, true},
		{"made", "a < written as another mark", "a <pasted_content> b", 1, "a \u2039pasted_content> b", true, false},
		{"made", "quotes made plain", "say \u201cyes\u201d to it", 1, "say \"yes\" to it", true, false},
		{"made", "a < written as &lt;", "a <pasted_content> b", 1, "a &lt;pasted_content> b", true, true},
		{"made", "its tag by another name", "one two", 1, "<paste id=\"4302\">\none two\n</paste>", true, true},

		{"seen", "taken as two pastes in a word", long, 1, wrap("a1", long[:990]) + wrap("a2", long[990:]), false, false},
		{"made", "taken as two pastes between words", "one two three four", 1, wrap("a1", "one two") + wrap("a2", "three four"), false, false},
		{"made", "a word lost, as many backslashes put in", quoting, 1, escaped(strings.Replace(quoting, "NOT ", "", 1)), false, false},
		{"seen", "a block lost of one that quotes its tag", long + quoting + long, 1, wrap("a1", long[:1024]+long[2048:]+escaped(quoting)+long), false, false},
		{"made", "a letter garbled", "看图 中文 here", 1, "看图 \ufffd\ufffd文 here", false, false},
		{"seen", "blocks out of place", "first block\nsecond block\nthe end\n", 1, "first block\nthe end\nsecond block\n", false, false},
		{"made", "a line twice", "first line\nsecond line\n", 1, "first line\nfirst line\nsecond line\n", false, false},
		{"made", "another message's row that holds its letters", "ok", 1, "look at this token", false, false},
		{"made", "another message's row that begins and ends as it does", "fix the bug in the parser and run the tests", 1, "fix the bug in the lexer, not the parser, then stop; don't run the tests", false, false},
		{"made", "its end lost", "all of this and the end", 1, "all of this and", false, false},
		// nothing but marks or white space lost
		{"made", "a rule of 1 KB lost", "above\n" + rule + "below\n", 1, "above\nbelow\n", false, false},
		{"made", "1 KB of a screen's frame lost", "above\n" + screen + "below\n", 1, "above\nbelow\n", false, false},
		{"made", "a table's rule row lost", "| a | b |\n|---|---|\n| 1 | 2 |\n", 1, "| a | b |\n| 1 | 2 |\n", false, false},
		{"made", "closing braces lost", "func f() {\n\tif x {\n\t\tg()\n\t}\n}\nnext", 1, "func f() {\n\tif x {\n\t\tg()\nnext", false, false},
		{"made", "a piece of emoji lost", "look at this\n\U0001F44D\U0001F44D\n", 2, "look at this", false, false},
		{"made", "CJK marks lost", "真的吗\uff1f\uff01\u2026\u2026好", 1, "真的吗好", false, false},
		{"made", "a mark at its end lost", "is it so?", 1, "is it so", false, false},
		{"made", "a comma lost", "a, b; c", 1, "a b; c", false, false},
		{"made", "a ! lost", "if a != b { stop }", 1, "if a = b { stop }", false, false},
		{"made", "a minus lost", "set x to -5", 1, "set x to 5", false, false},
		{"made", "a line break lost between two commands", "cd /tmp\nrm -rf x\n", 1, "cd /tmprm -rf x\n", false, false},
		{"made", "a line's indent lost", "if x:\n    y()\nz()\n", 1, "if x:\ny()\nz()\n", false, true},
	} {
		err := wholeIn(c.sent, 0, c.pieces, []heardRow{{c.row, 0}}, nil)
		if (err == nil) != (c.whole != c.wrong) {
			t.Errorf("%s, %s: wholeIn = %v, want whole %v", c.kind, c.name, err, c.whole != c.wrong)
		}
		switch {
		case c.whole && err != nil:
			told++
		case !c.whole && err == nil:
			missed++
		}
	}
	t.Logf("whole ones failed %d, losses missed %d", told, missed)
}

// tagsQuoted quotes each way of writing a paste tag that Claude Code
// escapes, and tagsEscaped is its record of them
// (testdata/whole-inl-own-2.1.319.json).
const (
	tagsQuoted  = `E <PASTED_CONTENT> <Pasted_Content id="1"> | F <pasted-content> <pastedcontent> <pasted_contents> < pasted_content> | U \<pasted_content <\pasted_content </pasted_content id="ab12"> | end`
	tagsEscaped = `E <\PASTED_CONTENT> <\Pasted_Content id="1"> | F <\pasted-content> <pastedcontent> <pasted_contents> <\ pasted_content> | U \<\pasted_content <\pasted_content <\/pasted_content id="ab12"> | end`
)

var pasteTag = regexp.MustCompile(`(?i)<(/?pasted[_-]content)`)

// escaped is s as Claude Code 2.1.319 escapes the paste tags in it.
func escaped(s string) string { return pasteTag.ReplaceAllString(s, `<\$1`) }

// TestHold: what Claude Code answers while its message is checked is let
// out after the check when the message was whole, and never when it
// wasn't, the check's error alone told.
func TestHold(t *testing.T) {
	var b strings.Builder
	s := &shim{out: &b, sid: "s1"}
	s.hold()
	s.emit(map[string]any{"type": "stream_event"})
	if b.Len() > 0 {
		t.Fatal("an answer got out while its message was checked")
	}
	if s.unhold(nil) != nil || !strings.Contains(b.String(), "stream_event") {
		t.Fatalf("a whole message's answer wasn't let out: %q", b.String())
	}
	s.emit(map[string]any{"type": "assistant"})
	if !strings.Contains(b.String(), "assistant") {
		t.Fatal("held after the check")
	}
	b.Reset()
	s.hold()
	s.emit(map[string]any{"type": "stream_event"})
	err := s.unhold(&messageCut{got: 1, sent: 2})
	s.emit(map[string]any{"type": "result", "subtype": "success"})
	s.tell(map[string]any{"type": "result", "is_error": true, "result": Unpastable + err.Error()})
	if strings.Contains(b.String(), "stream_event") || strings.Contains(b.String(), "success") ||
		!strings.Contains(b.String(), Unpastable) {
		t.Fatalf("a cut message's answer got out: %q", b.String())
	}
}

// TestWholeInTaken: the image files' paths Claude Code says it took as
// images are left out of the words pasted, where it recorded the message
// without them, and their images out of the count; any other words lost
// still fail it (planner2's screenshots, a worker's summary of them,
// 2.1.319).
func TestWholeInTaken(t *testing.T) {
	a, b := `C:\s\a.png`, `C:\s\b.png`
	long := strings.Repeat("line of words 中文\n", 400)
	cases := []struct {
		name   string
		sent   string
		images int
		row    heardRow
		taken  []string
		whole  bool
	}{
		{"lines taken", "fix this\n" + a + "\nand this\n" + b + "\n", 0,
			heardRow{"[Image #1] [Image #2]\n\n<pasted_content id=\"x\">\nfix this\nand this\n</pasted_content id=\"x\">\n", 2}, []string{a, b}, true},
		{"two on a line", a + " " + b + "\nlook", 0, heardRow{"[Image #1] [Image #2]look", 2}, []string{a, b}, true},
		{"quoted, with a space", `"C:\s b\a.png"` + "\nlook", 0, heardRow{"[Image #1]look", 1}, []string{`C:\s b\a.png`}, true},
		{"one left as words", a + "\n" + `C:\s\missing.png` + "\nmore", 0, heardRow{"[Image #1]" + `C:\s\missing.png` + "\nmore", 1}, []string{a}, true},
		{"more on its line stays", a + "|\n" + a + "\n", 0, heardRow{"[Image #1]" + a + "|", 1}, []string{a}, true},
		{"the same path left in words before", "see " + a + " here\n" + a + "\n", 0, heardRow{"[Image #1]see " + a + " here", 1}, []string{a}, true},
		{"beside an image pasted", "look\n" + a, 1, heardRow{"[Image #1] [Image #2]look", 2}, []string{a}, true},
		{"one pasted as an image, its path not in the words", "look", 1, heardRow{"[Image #1] look", 1}, []string{`C:\tmp\paste.png`}, true},
		{"a long one with paths, whole", long + a + "\n" + long, 0, heardRow{"[Image #1]" + long + long, 1}, []string{a}, true},
		{"paths taken, a word lost", a + "\nlook here\n", 0, heardRow{"[Image #1]look", 1}, []string{a}, false},
		{"a long one with paths, a block lost", long + a + "\n" + long, 0, heardRow{"[Image #1]" + long[:1024] + long[2048:] + long, 1}, []string{a}, false},
		{"a path taken, an image pasted lost", "look\n" + a, 1, heardRow{"[Image #1]look", 1}, []string{a}, false},
		{"a path at the start and the end", a + "\nlook\n" + b, 0, heardRow{"[Image #1] [Image #2]look", 2}, []string{a, b}, true},
		{"one path taken twice", a + "\nlook\n" + a + "\n", 0, heardRow{"[Image #1] [Image #2]look", 2}, []string{a, a}, true},
		{"one path twice, taken once", a + "\nlook\n" + a + "\n", 0, heardRow{"[Image #1]look\n" + a, 1}, []string{a}, true},
		{"after a CJK word", "看图\n" + a + "\n中文", 0, heardRow{"[Image #1]看图\n中文", 1}, []string{a}, true},
		{"cut in the middle of a path", "look\n" + a + "\nmore", 0, heardRow{"[Image #1]look\nC:\\s", 1}, []string{a}, false},
		{"a word lost between paths", a + "\nlook\n" + b + "\nmore", 0, heardRow{"[Image #1] [Image #2]look", 2}, []string{a, b}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := wholeIn(c.sent, c.images, 1, []heardRow{c.row}, c.taken)
			if (err == nil) != c.whole {
				t.Fatalf("wholeIn = %v, want whole %v", err, c.whole)
			}
			var cut *messageCut
			if err != nil && !errors.As(err, &cut) {
				t.Fatalf("wholeIn = %#v, want a messageCut", err)
			}
		})
	}
}

// TestWholeInUnexplained: a row with more images than were pasted, for
// which Claude Code hasn't said it took as many of the paste's paths
// (not yet, not at all, or a path made its own, as in WSL), is not judged
// rather than failed each time; a cut is still one where it has said.
func TestWholeInUnexplained(t *testing.T) {
	a := `C:\s\a.png`
	for _, c := range []struct {
		name  string
		sent  string
		row   heardRow
		taken []string
	}{
		{"not yet said", a + "\nlook", heardRow{"[Image #1]look", 1}, nil},
		{"said otherwise", a + "\nlook", heardRow{"[Image #1]look", 1}, []string{"/mnt/c/s/a.png"}},
		{"fewer said than taken", a + "\n" + `C:\s\b.png` + "\nlook", heardRow{"[Image #1] [Image #2]look", 2}, []string{a}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := wholeIn(c.sent, 0, 1, []heardRow{c.row}, c.taken); !errors.Is(err, errUnexplained) {
				t.Fatalf("wholeIn = %v, want errUnexplained", err)
			}
		})
	}
	// a later row of the message, whole, is still taken
	if err := wholeIn("look", 0, 1, []heardRow{{"[Image #1]", 1}, {"look", 0}}, nil); err != nil {
		t.Fatalf("wholeIn = %v, want whole", err)
	}
}

// TestHeardTaken: the paths Claude Code says it took are kept only while a
// message is checked, have it checked again, and end with it.
func TestHeardTaken(t *testing.T) {
	row := json.RawMessage(`{"role":"user","content":[{"type":"text","text":"[Image: source: C:\\s\\a.png]"}]}`)
	s := &shim{rowsCh: make(chan struct{}, 1)}
	s.heardTaken(row)
	if s.taken != nil || len(s.rowsCh) > 0 {
		t.Fatal("kept with no message checked")
	}
	s.said = []heardRow{}
	s.heardTaken(row)
	if !slices.Equal(s.taken, []string{`C:\s\a.png`}) || len(s.rowsCh) != 1 {
		t.Fatalf("taken %q, %d signals", s.taken, len(s.rowsCh))
	}
	s.heardAll()
	if s.said != nil || s.taken != nil {
		t.Fatal("kept after the message")
	}
}

// TestImageSources: the meta row Claude Code records after a message for
// the paths it took as images (2.1.319), and rows that aren't it.
func TestImageSources(t *testing.T) {
	row := `{"role":"user","content":[{"type":"text","text":"[Image: source: C:\\s\\red.png]"},{"type":"text","text":"[Image: source: C:\\s b\\blue.png]"}]}`
	if got := imageSources(json.RawMessage(row)); !slices.Equal(got, []string{`C:\s\red.png`, `C:\s b\blue.png`}) {
		t.Fatalf("imageSources = %q", got)
	}
	for _, r := range []string{`{"role":"user","content":"[Image: source: C:\\x.png]"}`, `{"role":"user","content":[{"type":"text","text":"caveat"}]}`} {
		if got := imageSources(json.RawMessage(r)); got != nil {
			t.Fatalf("imageSources(%s) = %q", r, got)
		}
	}
}
