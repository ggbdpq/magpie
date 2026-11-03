package claudetui

import (
	"io"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
)

// imageTerminal feeds each chunk through shim.read as a separate PTY
// read. A paste can redraw just one cell, with no complete marker in the
// raw output left after clearScreen.
type imageTerminal struct {
	chunks  []string
	writes  []string
	onWrite func()
}

func (p *imageTerminal) Read(b []byte) (int, error) {
	if len(p.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(b, p.chunks[0])
	p.chunks[0] = p.chunks[0][n:]
	if p.chunks[0] == "" {
		p.chunks = p.chunks[1:]
	}
	return n, nil
}

func (p *imageTerminal) Write(b []byte) (int, error) {
	p.writes = append(p.writes, string(b))
	if p.onWrite != nil {
		p.onWrite()
	}
	return len(b), nil
}

func imageShim(initial string, redraw ...string) (*shim, *imageTerminal) {
	p := &imageTerminal{chunks: []string{initial}}
	s := &shim{pty: p, changed: make(chan struct{}, 1), exited: make(chan struct{})}
	s.read()
	p.onWrite = func() {
		p.chunks = slices.Clone(redraw)
		s.read()
	}
	return s, p
}

func TestPasteImageAcceptsIncrementalRedraw(t *testing.T) {
	for name, redraw := range map[string][]string{
		"one read":   {"\x1b[1;12H8"},
		"split ANSI": {"\x1b", "[1;", "12", "H", "8"},
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, p := imageShim("❯ [Image #17]", redraw...)
				n, err := s.pasteImage("image-18.png", 17)
				if err != nil || n != 18 {
					t.Fatalf("incrementally drawn image was not acknowledged: number=%d err=%v", n, err)
				}
				if want := []string{"\x1b[200~image-18.png\x1b[201~"}; !slices.Equal(p.writes, want) {
					t.Fatalf("image path was not pasted exactly once: %q", p.writes)
				}
				if strings.Contains(s.plain(), "[Image #18]") {
					t.Fatal("fixture unexpectedly contains a complete marker in the raw output")
				}
			})
		})
	}
}

func TestPasteImageRejectsOldScreenMarkers(t *testing.T) {
	for name, screen := range map[string]string{
		"history":   "❯ [Image #18]\r\nanswer\r\n❯ ",
		"no prompt": "[Image #18]",
		"unchanged": "❯ [Image #17]",
		"older":     "❯ [Image #16]",
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, p := imageShim(screen, "\x1b[2J\x1b[H"+screen)
				n, err := s.pasteImage("image-18.png", 17)
				if n != 0 || err == nil || !strings.Contains(err.Error(), "did not take an image") {
					t.Fatalf("stale marker acknowledged: number=%d err=%v", n, err)
				}
				if want := []string{"\x1b[200~image-18.png\x1b[201~"}; !slices.Equal(p.writes, want) {
					t.Fatalf("failed paste was retried or submitted: %q", p.writes)
				}
			})
		})
	}
}

func TestImageRedrawGridKeepsSplitUTF8AndErases(t *testing.T) {
	s, p := imageShim("❯ [Image #17]")
	s.clearScreen()
	// Erase the old chip, split the Chinese character across reads, and
	// draw the new chip after its two terminal cells.
	p.chunks = []string{"\x1b[1;3H\x1b[K\xe4", "\xb8", "\xad [Image #18]"}
	s.read()
	if n := s.imageAfter(17); n != 18 {
		t.Fatalf("split UTF-8 lost the image: number=%d", n)
	}
	now, _ := s.looks()
	if !strings.HasPrefix(now, "❯ 中 [Image #18]\n") {
		t.Fatalf("wide character misdrawn: %q", now)
	}
	// Column 6 follows the prompt, space, wide character and space.
	p.chunks = []string{"\x1b[1;6H\x1b[K"}
	s.read()
	if n := s.imageAfter(0); n != 0 {
		t.Fatalf("erased image still acknowledged: number=%d", n)
	}
	p.chunks = []string{"\x1b[2J"}
	s.read()
	if now, _ := s.looks(); strings.TrimSpace(now) != "" {
		t.Fatalf("screen not erased: %q", now)
	}
}

func TestPasteImageFailureStopsSend(t *testing.T) {
	for _, exited := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			s, p := imageShim("❯ ")
			wantError := "did not take an image"
			if exited {
				close(s.exited)
				wantError = "Claude Code ended"
			}
			err := s.send([]piece{{image: "image-1.png"}, {text: "answer this"}})
			if err == nil || !strings.Contains(err.Error(), wantError) {
				t.Fatalf("unconfirmed image did not fail the send: %v", err)
			}
			if want := []string{"\x1b[200~image-1.png\x1b[201~"}; !slices.Equal(p.writes, want) {
				t.Fatalf("send continued after an unconfirmed image: %q", p.writes)
			}
		})
	}
}

func TestImageAfterCurrentPrompt(t *testing.T) {
	for _, tc := range []struct {
		name, screen string
		after, want  int
	}{
		{"wrapped input", "❯ words\r\n  [Image #18]", 17, 18},
		{"shared paste numbering", "  ❯ [Pasted text #18] [Image #19]", 17, 19},
		{"last prompt", "❯ [Image #99]\r\n❯ [Image #18]", 17, 18},
		{"unchanged before new", "❯ [Image #17] [Image #18]", 17, 18},
		{"overflow", "❯ [Image #999999999999999999999999999999]", 17, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := imageShim(tc.screen)
			if n := s.imageAfter(tc.after); n != tc.want {
				t.Fatalf("imageAfter(%d) = %d, want %d", tc.after, n, tc.want)
			}
		})
	}
	if n := (&shim{}).imageAfter(0); n != 0 {
		t.Fatalf("unread screen acknowledged an image: %d", n)
	}
}
