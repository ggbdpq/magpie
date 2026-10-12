package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A gateway-sessions-hidden.json that doesn't parse holds deletions magpie
// made before, not nothing: the next DeleteGateway or Restore keeps it aside
// (as stash.json's and logins.json's unread files are kept) instead of
// writing the list over, so what it held can still be got back.
func TestUnreadHiddenKeptAside(t *testing.T) {
	gatewayHome(t)
	last := time.Now().Add(-2 * time.Hour)
	if _, err := DeleteGateway("claude", "gone", "gone", last); err != nil {
		t.Fatal(err)
	}
	p := gatewayHiddenPath()
	was, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(was), `"gone"`) {
		t.Fatalf("the hidden file lost the first deletion: %s", was)
	}
	// half a write, as an older magpie's cut-short one would leave
	bad := was[:len(was)/2]
	if err := os.WriteFile(p, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DeleteGateway("claude", "kept", "kept", last); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(p + ".bad-*")
	if len(matches) == 0 {
		t.Fatal("the unread hidden file was written over, not kept aside")
	}
	kept, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != string(bad) {
		t.Fatalf("the file kept aside isn't the one that was there: %s", kept)
	}
}
