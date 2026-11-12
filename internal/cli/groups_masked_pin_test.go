package cli

import (
	"strings"
	"testing"
)

// A member pinned to an account whose shown name carries stars went into
// the group's patterns as a glob: IsPattern judged the member whole, a
// masked name's `*`s and all, so `workbuddy/deepseek-v4.1-flash#1276-762****491`
// became a pattern matching no id — and the member was dropped without a
// word (#6131). The pattern is judged on the model the member names now,
// so a pin that names no account is refused the way a typo is, and a
// pattern typed among the members is still one.
func TestGroupAddMemberWithMaskedAccountRefused(t *testing.T) {
	groupsHome(t)

	_, err := addGroup("wb", []string{"models=a/m#1276-762****491,b/vendor/m"})
	if err == nil || !strings.Contains(err.Error(), "1276-762****491") {
		t.Fatalf("a masked pin that names no account: %v", err)
	}
	// nothing was saved: the member is not in the group turned into a
	// pattern that matches nothing
	if gs := storedGroups(t); len(gs) != 0 {
		t.Fatalf("saved anyway: %v", gs)
	}

	// a pattern typed among the members is still one, stars and all
	if _, err := addGroup("pat", []string{"models=a/*,b/vendor/m"}); err != nil {
		t.Fatalf("a pattern among the members: %v", err)
	}
	gs := storedGroups(t)
	if len(gs) != 1 {
		t.Fatalf("stored: %v", gs)
	}
	match, _ := gs[0]["match"].([]any)
	if len(match) != 1 || match[0] != "a/*" {
		t.Fatalf("pattern not kept: %v", gs[0])
	}
	if m, _ := gs[0]["members"].([]any); len(m) != 1 || m[0] != "b/vendor/m" {
		t.Fatalf("members: %v", gs[0])
	}
	// a pin that names no account of the provider is refused as a typo
	// is — never turned into a pattern that swallows it
	if _, err := addGroup("pin", []string{"models=a/m#57504284567c73ca,b/vendor/m"}); err == nil {
		t.Fatal("a pin on an account a has not was taken")
	}
}
