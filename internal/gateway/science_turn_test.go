package gateway

import (
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// scienceTurn's verdict: nothing picked, an unserved ask goes to the first
// model listed to Science; a turn notes its model, kept on disk; an
// unserved ask then goes to the session's model; a served ask is as asked.
func TestClaudeScienceTurn(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	sciencePicked.Lock()
	sciencePicked.model, sciencePicked.from = "", ""
	sciencePicked.Unlock()
	t.Cleanup(func() {
		sciencePicked.Lock()
		sciencePicked.model, sciencePicked.from = "", ""
		sciencePicked.Unlock()
	})
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Anthropic: "http://127.0.0.1:1",
		Models: []string{"m1", "claude-opus-4-8"}}); err != nil {
		t.Fatal(err)
	}
	turn := []byte(`{"max_tokens":32000,"tools":[{"name":"Bash"}],"messages":[{"role":"user","content":"hi"}]}`)
	title := []byte(`{"max_tokens":200,"messages":[{"role":"user","content":"hi"}]}`)
	if got := scienceTurn("gpt-9", title); got != "fake/m1" {
		t.Fatalf("unserved with nothing picked = %q, want fake/m1", got)
	}
	if _, err := os.Stat(sciencePickedPath()); err == nil {
		t.Fatal("an unserved ask picked a model")
	}
	if got := scienceTurn("fake/m1", turn); got != "fake/m1" {
		t.Fatalf("turn = %q, want fake/m1", got)
	}
	if b, err := os.ReadFile(sciencePickedPath()); err != nil || strings.TrimSpace(string(b)) != "fake/m1" {
		t.Fatalf("picked %q (%v), want fake/m1", b, err)
	}
	if got := scienceTurn("gpt-9", title); got != "fake/m1" {
		t.Fatalf("unserved after a turn = %q, want the session's fake/m1", got)
	}
	if got := scienceTurn("fake/claude-opus-4-8", title); got != "fake/claude-opus-4-8" {
		t.Fatalf("served ask = %q, want it as asked", got)
	}
	if b, _ := os.ReadFile(sciencePickedPath()); strings.TrimSpace(string(b)) != "fake/m1" {
		t.Fatalf("a title for a served ask kept %q", b)
	}
	if got := scienceTurn("fake/claude-opus-4-8", turn); got != "fake/claude-opus-4-8" {
		t.Fatalf("turn on opus = %q, want fake/claude-opus-4-8", got)
	}
	if b, _ := os.ReadFile(sciencePickedPath()); strings.TrimSpace(string(b)) != "fake/claude-opus-4-8" {
		t.Fatalf("kept %q, want fake/claude-opus-4-8", b)
	}
}
