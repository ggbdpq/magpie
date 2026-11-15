package provider

import "testing"

// clone feeds every diff-two-snapshots caller (Edit builds was and now from
// it, and a CLI "provider set resolve.…=…" is one). A map clone() leaves
// shared makes such a diff blind to Resolve changes: the edit lands in both
// snapshots, storedChanges sees nothing, and the second set is dropped
// without an error. qiqll's provider set (through Edit) was the first
// caller to trip on this; clone must hand back a Resolve nothing shares.
func TestProviderCloneCopiesResolve(t *testing.T) {
	p := Provider{Name: "corp", Resolve: map[string][]string{"gw.example": {"10.0.0.1:8080"}}}

	c := p.clone()
	c.Resolve["gw.example"][0] = "10.0.0.2"
	c.Resolve["other.example"] = []string{"10.0.0.3"}

	if got := p.Resolve["gw.example"][0]; got != "10.0.0.1:8080" {
		t.Fatalf("clone shares the inner slice: the original's address now reads %q", got)
	}
	if _, ok := p.Resolve["other.example"]; ok {
		t.Fatalf("clone shares the map: a host added to the copy leaked into the original")
	}
}
