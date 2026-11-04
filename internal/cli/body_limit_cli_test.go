package cli

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #4282: `magpie provider body-limit <id> 28mib` sets the most bytes one
// request to the provider may take, a plain number counts bytes, and off
// takes the limit away.
func TestBodyLimitCmd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "xk", Name: "xk", Chat: "https://example.invalid/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	limit := func() int {
		p, err := provider.Find("xk")
		if err != nil {
			t.Fatal(err)
		}
		return p.BodyLimit
	}
	if err := bodyLimitCmd([]string{"xk", "28mib"}); err != nil || limit() != 28<<20 {
		t.Fatalf("28mib: %d (%v)", limit(), err)
	}
	if err := bodyLimitCmd([]string{"xk", "1 MiB"}); err != nil || limit() != 1<<20 {
		t.Fatalf("1 MiB: %d (%v)", limit(), err)
	}
	if err := bodyLimitCmd([]string{"xk", "2048"}); err != nil || limit() != 2048 {
		t.Fatalf("2048: %d (%v)", limit(), err)
	}
	if err := bodyLimitCmd([]string{"xk", "off"}); err != nil || limit() != 0 {
		t.Fatalf("off: %d (%v)", limit(), err)
	}
	if err := bodyLimitCmd([]string{"xk", "28zb"}); err == nil || limit() != 0 {
		t.Fatal("28zb was taken")
	}
	if err := bodyLimitCmd([]string{"xk", "-5"}); err == nil || limit() != 0 {
		t.Fatal("-5 was taken")
	}
	if err := bodyLimitCmd([]string{"nosuch", "1mib"}); err == nil {
		t.Fatal("an unknown provider was given a limit")
	}
	if err := bodyLimitCmd([]string{"xk"}); err != nil {
		t.Fatalf("reading alone: %v", err)
	}
}
