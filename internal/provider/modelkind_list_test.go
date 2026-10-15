package provider

import (
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// A model the user said makes videos or images (ModelKinds, SetModelKind)
// leaves the chat list the way a vendor-marked one does: said video reads
// as Films (never offered to talk to), said image as Draws (Settings →
// Images only); a model they said nothing of keeps the vendor's word.
// #2071: a relay's omni_flash-10s kept its "Video model" label and still
// sat in the chat list.
func TestUserModelKindMovesTheList(t *testing.T) {
	prefsHome(t)
	p := Provider{ID: "relay", Chat: "http://127.0.0.1:9/v1"}
	if !p.MarksKinds() {
		t.Fatal("the test's provider can't take kinds")
	}
	s := settings.Load()
	s.ModelKinds = map[string]string{"relay/omni_flash-10s": KindVideo, "relay/user-image": KindImage}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	ms := []catalog.Model{
		{ID: "omni_flash-10s", Name: "Omni Flash"},
		{ID: "text-model", Name: "Text", Draws: true},
		{ID: "user-image", Name: "User Image"},
	}
	byID := map[string]catalog.Model{}
	for _, m := range p.withUserKinds(ms) {
		byID[m.ID] = m
	}
	if !byID["omni_flash-10s"].Films || byID["omni_flash-10s"].Draws {
		t.Fatalf("the video kind didn't make omni_flash-10s Films: %+v", byID["omni_flash-10s"])
	}
	if !byID["user-image"].Draws || byID["user-image"].Films {
		t.Fatalf("the image kind didn't make user-image Draws: %+v", byID["user-image"])
	}
	if !byID["text-model"].Draws || byID["text-model"].Films {
		t.Fatalf("an unsaid model lost the vendor's word: %+v", byID["text-model"])
	}
}

// An Available() pass applies the same: a provider's list carries the
// user's kinds to every consumer of the list, Exposed and the gateway's
// included.
func TestAvailableCarriesUserKinds(t *testing.T) {
	prefsHome(t)
	p := Provider{ID: "relay", Chat: "http://127.0.0.1:9/v1"}
	s := settings.Load()
	s.ModelKinds = map[string]string{"relay/omni_flash-10s": KindVideo}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	// no list fetched and none in the catalog: Available is empty, and the
	// kind stays kept for when a list arrives; with a hand-built list the
	// flag lands
	p.Models = []string{"omni_flash-10s"}
	ms := p.withUserKinds([]catalog.Model{{ID: "omni_flash-10s", Name: "Omni Flash"}})
	if len(ms) != 1 || !ms[0].Films {
		t.Fatalf("the list lost the user's video kind: %+v", ms)
	}
}
