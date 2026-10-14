package library

import (
	"path/filepath"
	"slices"
	"testing"
)

// An agent uninstalled outside magpie leaves its folder behind (#2177,
// #2173): the Agents page calls it gone (CLIMissing) and offers none of
// its settings, since nothing reads them. The Library kept it as a place
// to give skills, MCP servers and instructions to, and sync carried on
// writing there.
func TestTargetsLeaveGoneAgentsOut(t *testing.T) {
	h := sandbox(t)
	// Hermes by its config.yaml alone (the reporter's 37 bytes): its
	// folder is kept by nothing here — no hermes on the sandbox's PATH,
	// no hermes-agent checkout beside the config
	write(t, filepath.Join(h, ".hermes", "config.yaml"), "custom_providers: []\nmcp_servers: {}\n")
	if got := ids(Targets()); slices.Contains(got, "hermes") {
		t.Fatalf("hermes is a target with its CLI gone: %v", got)
	}
}
