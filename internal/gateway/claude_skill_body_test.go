package gateway

import (
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

// Claude Code answers a Skill call with "Launching skill: pdf" and sends
// the skill's SKILL.md after the results, as a text block headed "Base
// directory for this skill: …". The run's model is handed it in the
// Skill call's result, and the user is not said to have sent it.
func TestSkillBodyReachesTheSkillResult(t *testing.T) {
	body := "Base directory for this skill: /home/u/.claude/skills/pdf\n\n# PDF\n\nUse pdftotext."
	msgs := []Message{
		{Role: "user", Parts: []Part{{Kind: Text, Text: "read a.pdf"}}},
		{Role: "assistant", Parts: []Part{
			{Kind: ToolCall, CallID: "c1", Name: "Read"},
			{Kind: ToolCall, CallID: "c2", Name: "Skill"},
		}},
		{Role: "user", Parts: []Part{
			{Kind: ToolResult, CallID: "c1", Text: "a.pdf is binary"},
			{Kind: ToolResult, CallID: "c2", Text: "Launching skill: pdf"},
			{Kind: Text, Text: body},
		}},
	}
	results := msgs[2].Parts[:2]
	got := withSkillBodies(results, msgs)
	if !strings.Contains(got[1].Text, "Use pdftotext.") || !strings.HasPrefix(got[1].Text, "Launching skill: pdf") {
		t.Fatalf("the Skill result doesn't carry the skill's body: %q", got[1].Text)
	}
	if strings.Contains(got[0].Text, "pdftotext") {
		t.Fatalf("the body went to the Read result: %q", got[0].Text)
	}
	if results[1].Text != "Launching skill: pdf" {
		t.Fatal("the request's own result was changed in place")
	}
	if said := saidWithResults(msgs); said != "" {
		t.Fatalf("the skill's body went to the run as the user's: %q", said)
	}

	// in a reminder, and with no Skill call to match: the last result
	msgs[1].Parts[1].Name = "Other"
	msgs[2].Parts[2].Text = "<system-reminder>\n" + body + "\n</system-reminder>"
	got = withSkillBodies(results, msgs)
	if !strings.HasSuffix(got[1].Text, "Use pdftotext.") {
		t.Fatalf("no Skill call: %q", got[1].Text)
	}

	// nothing after the results: the same results
	msgs[2].Parts = msgs[2].Parts[:2]
	if got := withSkillBodies(results, msgs); &got[0] != &results[0] {
		t.Fatal("no body, yet the results were copied")
	}
}

// Two skills launched at once each get their own body.
func TestSkillBodiesTwoSkills(t *testing.T) {
	msgs := []Message{
		{Role: "assistant", Parts: []Part{
			{Kind: ToolCall, CallID: "a", Name: "Skill"},
			{Kind: ToolCall, CallID: "b", Name: "Skill"},
		}},
		{Role: "user", Parts: []Part{
			{Kind: ToolResult, CallID: "a", Text: "Launching skill: pdf"},
			{Kind: ToolResult, CallID: "b", Text: "Launching skill: xlsx"},
			{Kind: Text, Text: "Base directory for this skill: /s/pdf\n\nPDF body"},
			{Kind: Text, Text: "Base directory for this skill: /s/xlsx\n\nXLSX body"},
		}},
	}
	got := withSkillBodies(msgs[1].Parts[:2], msgs)
	if !strings.HasSuffix(got[0].Text, "PDF body") || !strings.HasSuffix(got[1].Text, "XLSX body") {
		t.Fatalf("%q / %q", got[0].Text, got[1].Text)
	}
}

// The request Claude Code 2.1.316 sent after a Skill call, as it came
// (paths shortened): the body reaches the run with the Skill result.
func TestSkillBodyRealClaudeCode(t *testing.T) {
	b, err := os.ReadFile("testdata/claude-code-skill-launch.json")
	if err != nil {
		t.Fatal(err)
	}
	r, err := parseAnthropic(b)
	if err != nil {
		t.Fatal(err)
	}
	last := r.Messages[len(r.Messages)-1]
	var results []Part
	for _, p := range last.Parts {
		if p.Kind == ToolResult {
			results = append(results, p)
		}
	}
	got := withTaskNotices(withSkillBodies(results, r.Messages), r.Messages)
	if len(got) != 1 || !strings.HasPrefix(got[0].Text, "Launching skill: magpie-skills\n\nBase directory for this skill: ") ||
		!strings.Contains(got[0].Text, "/Users/u/.claude/skills/magpie-skills/.skills/greet-user/SKILL.md") {
		t.Fatalf("the run is handed: %+v", got)
	}
	if said := saidWithResults(r.Messages); said != "" {
		t.Fatalf("said as the user's: %q", said)
	}
}

// A skill whose body pushes the result past Claude Code's persistence
// line: the client saves a text result above 50,000 characters to a file
// on its own home and hands the model a reference instead — under the
// gateway's project and session, which a client on another computer
// cannot read (the reporter's Bash and Read both found nothing there,
// #7178). The body is cut to keep the result under the line, with a word
// of what was left out; a body that fits is handed whole, as before.
func TestSkillBodyKeepsTheResultUnderThePersistenceLine(t *testing.T) {
	build := func(body string) ([]Part, []Message) {
		msgs := []Message{
			{Role: "user", Parts: []Part{{Kind: Text, Text: "run the skill"}}},
			{Role: "assistant", Parts: []Part{{Kind: ToolCall, CallID: "c1", Name: "Skill"}}},
			{Role: "user", Parts: []Part{
				{Kind: ToolResult, CallID: "c1", Text: "Launching skill: big"},
				{Kind: Text, Text: body},
			}},
		}
		return msgs[2].Parts[:1], msgs
	}
	long := "Base directory for this skill: /home/u/.claude/skills/big\n\n" + strings.Repeat("x", 80_000)
	results, msgs := build(long)
	got := withSkillBodies(results, msgs)
	if len(got[0].Text) > 50_000 {
		t.Fatalf("a %d-byte result: it will be saved to the client's home, where the reference reads nothing", len(got[0].Text))
	}
	if !strings.Contains(got[0].Text, "left out") {
		t.Fatalf("the cut was not said: %q", got[0].Text[len(got[0].Text)-200:])
	}
	short := "Base directory for this skill: /home/u/.claude/skills/small\n\nUse it well."
	results, msgs = build(short)
	got = withSkillBodies(results, msgs)
	if !strings.HasSuffix(got[0].Text, "Use it well.") {
		t.Fatalf("a body that fits was cut: %q", got[0].Text)
	}

	// Multi-byte UTF-8 body cuts cleanly on a rune boundary and stays under the line through mcpResult.
	cjk := "Base directory for this skill: /home/u/.claude/skills/cjk\n\n" + strings.Repeat("中", 25_000)
	results, msgs = build(cjk)
	got = withSkillBodies(results, msgs)
	if len(got[0].Text) > 50_000 {
		t.Fatalf("a %d-byte UTF-8 result exceeds the persistence line", len(got[0].Text))
	}
	if !utf8.ValidString(got[0].Text) {
		t.Fatal("cutting a multi-byte UTF-8 skill body split a rune")
	}
	if !strings.Contains(got[0].Text, "left out") {
		t.Fatalf("the cut was not said for UTF-8 body")
	}
	res := mcpResult(got[0])
	if text, _ := res.Content[0]["text"].(string); len(text) > 50_000 || !strings.Contains(text, "Base directory for this skill: /home/u/.claude/skills/cjk") {
		t.Fatalf("mcpResult lost skill head or exceeded line: len=%d", len(text))
	}

	// Already-full result leaves zero room: must not panic on negative slice bound.
	fullMsgs := []Message{
		{Role: "user", Parts: []Part{{Kind: Text, Text: "run the skill"}}},
		{Role: "assistant", Parts: []Part{{Kind: ToolCall, CallID: "c1", Name: "Skill"}}},
		{Role: "user", Parts: []Part{
			{Kind: ToolResult, CallID: "c1", Text: strings.Repeat("a", 50_000)},
			{Kind: Text, Text: long},
		}},
	}
	got = withSkillBodies(fullMsgs[2].Parts[:1], fullMsgs)
	if !strings.Contains(got[0].Text, "left out") {
		t.Fatalf("already-full result did not note the cut")
	}
}
