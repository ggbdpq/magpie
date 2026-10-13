package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

// A run started anew for a conversation with replies in it is told it in
// one message: the turns answered already are marked as such, with what
// was sent in them, apart from the turn to answer now (#1365). Told as one
// stretch of Human:/Assistant: text, every image in it read as just sent.
func TestClaudePromptMarksEarlierTurns(t *testing.T) {
	img := Part{Kind: Image, MediaType: "image/png", Data: "iVBORw0KGgo="}
	req := &Request{Messages: []Message{
		{Role: "user", Parts: []Part{{Kind: Text, Text: "look at this"}, img}},
		{Role: "assistant", Parts: []Part{{Kind: Text, Text: "a cat"}}},
		{Role: "user", Parts: []Part{{Kind: Text, Text: "and this one"}, img}},
		{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: "c1", Name: "read", Args: json.RawMessage(`{}`)}}},
		{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: "c1", Text: "ok"}, img}},
	}}
	blocks, err := renderClaudePrompt(req)
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	var kinds []string
	for _, b := range blocks {
		if b["type"] == "image" {
			kinds = append(kinds, "image")
			all.WriteString("<IMG>")
			continue
		}
		s, _ := b["text"].(string)
		all.WriteString(s)
	}
	got := all.String()
	open, end := strings.Index(got, "<conversation_history>"), strings.Index(got, "</conversation_history>")
	cur := strings.Index(got, "<current_turn>")
	if open < 0 || end < open || cur < end || !strings.HasSuffix(got, "</current_turn>") {
		t.Fatalf("the turns aren't marked:\n%s", got)
	}
	history, now := got[open:end], got[cur:]
	if !strings.Contains(history, "Human: look at this\n[image sent with an earlier message]<IMG>") || !strings.Contains(history, "Assistant: a cat") || strings.Contains(history, "and this one") {
		t.Fatalf("history:\n%s", history)
	}
	if !strings.Contains(now, "Human: and this one<IMG>") || !strings.Contains(now, "[tool result id=c1]") || strings.Count(now, "<IMG>") != 2 {
		t.Fatalf("the turn to answer:\n%s", now)
	}

	// a first turn, or a conversation with no reply in it, is told as before
	for _, msgs := range [][]Message{req.Messages[:1], {req.Messages[0], req.Messages[0]}} {
		blocks, _ := renderClaudePrompt(&Request{Messages: msgs})
		b, _ := json.Marshal(blocks)
		if strings.Contains(string(b), "conversation_history") || strings.Contains(string(b), "current_turn") {
			t.Fatalf("marked a conversation with no reply: %s", b)
		}
	}
}

// Every image and file inside <conversation_history> is labeled where it
// sits with when it was sent, and the turn to answer's are not: the head
// note (historyOpen) says it once for the whole section, and eighteen
// images later the model took a history's tool images for new attachments
// and re-read two of them with view_image to find out (#1751). A
// conversation with no history is one turn of its own: nothing in it is
// labeled.
func TestClaudePromptLabelsEarlierImages(t *testing.T) {
	img := Part{Kind: Image, MediaType: "image/png", Data: "iVBORw0KGgo="}
	file := Part{Kind: File, MediaType: "application/pdf", URL: "https://x.test/a.pdf"}
	req := &Request{Messages: []Message{
		{Role: "user", Parts: []Part{{Kind: Text, Text: "look at this"}, img, file}},
		{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: "c1", Name: "view_image", Args: json.RawMessage(`{}`)}}},
		{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: "c1", Text: "ok"}, img}},
		{Role: "assistant", Parts: []Part{{Kind: Text, Text: "a cat"}}},
		{Role: "user", Parts: []Part{{Kind: Text, Text: "and this one"}, img, file}},
		{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: "c2", Name: "view_image", Args: json.RawMessage(`{}`)}}},
		{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: "c2", Text: "ok"}, img}},
	}}
	blocks, err := renderClaudePrompt(req)
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, b := range blocks {
		if b["type"] == "image" {
			all.WriteString("<IMG>")
			continue
		}
		s, _ := b["text"].(string)
		all.WriteString(s)
	}
	got := all.String()
	end, cur := strings.Index(got, "</conversation_history>"), strings.Index(got, "<current_turn>")
	if end < 0 || cur < end {
		t.Fatalf("the turns aren't marked:\n%s", got)
	}
	history, now := got[:end], got[cur:]
	if !strings.Contains(history, "Human: look at this\n[image sent with an earlier message]<IMG>") {
		t.Fatalf("the user's earlier image is not told as earlier:\n%s", history)
	}
	if !strings.Contains(history, "[tool result id=c1]\nok\n[image an earlier tool call returned]<IMG>") {
		t.Fatalf("the earlier tool's image is not told as its:\n%s", history)
	}
	if !strings.Contains(history, "[file sent with an earlier message][attachment application/pdf: https://x.test/a.pdf]") {
		t.Fatalf("the earlier file is not told as earlier:\n%s", history)
	}
	for _, label := range []string{"[image sent with an earlier message]", "[image an earlier tool call returned]", "[file sent with an earlier message]"} {
		if strings.Contains(now, label) {
			t.Fatalf("the turn to answer carries %s:\n%s", label, now)
		}
	}
	if strings.Count(now, "<IMG>") != 2 {
		t.Fatalf("the turn to answer's images:\n%s", now)
	}

	// a conversation with no reply that calls no tool is one turn of its
	// own: its images are the turn's, none labeled
	b, _ := json.Marshal(mustRender(t, &Request{Messages: req.Messages[:3]}))
	if strings.Contains(string(b), "sent with an earlier message") || strings.Contains(string(b), "an earlier tool call returned") {
		t.Fatalf("labeled a conversation with no history: %s", b)
	}
}
