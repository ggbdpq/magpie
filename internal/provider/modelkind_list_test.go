package provider

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// kindHome is a sandbox home with a relay whose fetched list says nothing
// of what its models make: omni_flash-10s and a chat model beside it, as
// the relay of #2071 lists them.
func kindHome(t *testing.T) Provider {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	p := Provider{ID: "relay", Name: "Relay", Chat: "http://127.0.0.1:9/v1", Key: "k"}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("relay", "http://127.0.0.1:9/v1", []catalog.Model{
		{ID: "omni_flash-10s", Name: "Omni Flash"},
		{ID: "claude-sonnet-4-5", Name: "Claude Sonnet 4.5"},
	}); err != nil {
		t.Fatal(err)
	}
	return p
}

func listOf(ms []catalog.Model) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

func hasModel(ms []catalog.Model, id string) bool {
	return slices.ContainsFunc(ms, func(m catalog.Model) bool { return m.ID == id })
}

// A model the user said makes videos or images (SetModelKind, #1399) leaves
// the chat list itself: the editor's chips, the agents' lists and the
// gateway's are all built on Available and Exposed, and a flag none of them
// read kept a marked model's seat in every one (#2071: a relay's
// omni_flash-10s, marked "Video model", went on showing where agents chat).
// Said back to Auto, it comes back.
func TestUserKindLeavesTheChatList(t *testing.T) {
	p := kindHome(t)
	if !hasModel(p.Available(), "omni_flash-10s") || !hasModel(p.Exposed(), "omni_flash-10s") {
		t.Fatalf("an unmarked model left the chat list: available %v, exposed %v", listOf(p.Available()), listOf(p.Exposed()))
	}
	if err := SetModelKind("relay/omni_flash-10s", KindVideo); err != nil {
		t.Fatal(err)
	}
	if hasModel(p.Available(), "omni_flash-10s") {
		t.Fatalf("Available (the editor's chat chips) still has it: %v", listOf(p.Available()))
	}
	if hasModel(p.Exposed(), "omni_flash-10s") {
		t.Fatalf("Exposed (the agents' lists) still has it: %v", listOf(p.Exposed()))
	}
	// the videos path keeps it: a marked model is asked at the videos API
	if got := p.KindModels(KindVideo); !slices.Equal(got, []string{"omni_flash-10s"}) {
		t.Fatalf("the videos path lost it: %v", got)
	}
	// said image, the same
	if err := SetModelKind("relay/claude-sonnet-4-5", KindImage); err != nil {
		t.Fatal(err)
	}
	if hasModel(p.Available(), "claude-sonnet-4-5") || hasModel(p.Exposed(), "claude-sonnet-4-5") {
		t.Fatalf("the image mark left it in the chat list: available %v, exposed %v", listOf(p.Available()), listOf(p.Exposed()))
	}
	if got := p.KindModels(KindImage); !slices.Equal(got, []string{"claude-sonnet-4-5"}) {
		t.Fatalf("the images path lost it: %v", got)
	}
	if err := SetModelKind("relay/omni_flash-10s", ""); err != nil {
		t.Fatal(err)
	}
	if !hasModel(p.Available(), "omni_flash-10s") || !hasModel(p.Exposed(), "omni_flash-10s") {
		t.Fatalf("said back to Auto it didn't come back: available %v, exposed %v", listOf(p.Available()), listOf(p.Exposed()))
	}
}

// A pick the user later said makes videos stays out of the agents' list all
// the same: Exposed builds a picked model the list no longer offers as an
// unlisted one, and the user's word beats the pick.
func TestUserKindBeatsAPick(t *testing.T) {
	p := kindHome(t)
	p.Models = []string{"omni_flash-10s", "claude-sonnet-4-5"}
	if err := SetModelKind("relay/omni_flash-10s", KindVideo); err != nil {
		t.Fatal(err)
	}
	if hasModel(p.Exposed(), "omni_flash-10s") {
		t.Fatalf("Exposed still serves a pick the user said makes videos: %v", listOf(p.Exposed()))
	}
	if !hasModel(p.Exposed(), "claude-sonnet-4-5") {
		t.Fatalf("Exposed lost a pick nothing was said of: %v", listOf(p.Exposed()))
	}
}
