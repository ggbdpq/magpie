package provider

import (
	"testing"
)

// A provider's body limit is 0 (off) unless it is set, is never negative,
// and is kept within a GiB (issue #4282).
func TestBodyLimitNormalize(t *testing.T) {
	if p := normalize(Provider{BodyLimit: 0}); p.BodyLimit != 0 {
		t.Fatalf("the default isn't off: %d", p.BodyLimit)
	}
	if p := normalize(Provider{BodyLimit: -4}); p.BodyLimit != 0 {
		t.Fatalf("a negative limit is %d, not off", p.BodyLimit)
	}
	if p := normalize(Provider{BodyLimit: 28 << 20}); p.BodyLimit != 28<<20 {
		t.Fatalf("28 MiB came out %d", p.BodyLimit)
	}
	if p := normalize(Provider{BodyLimit: MaxBodyLimit + 1}); p.BodyLimit != MaxBodyLimit {
		t.Fatalf("over the most came out %d, not %d", p.BodyLimit, MaxBodyLimit)
	}
}

// `SetBodyLimit` sets the most bytes one request to a provider may take,
// takes it back off, and refuses a limit it could never keep.
func TestSetBodyLimit(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := Save(Provider{ID: "bl", Name: "bl", Chat: "https://example.invalid/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := SetBodyLimit("bl", 28<<20); err != nil {
		t.Fatal(err)
	}
	p, err := Find("bl")
	if err != nil {
		t.Fatal(err)
	}
	if p.BodyLimit != 28<<20 {
		t.Fatalf("the limit is %d", p.BodyLimit)
	}
	if err := SetBodyLimit("bl", -1); err == nil {
		t.Fatal("a negative limit was taken")
	}
	if err := SetBodyLimit("bl", MaxBodyLimit+1); err == nil {
		t.Fatal("a limit over the most was taken")
	}
	if err := SetBodyLimit("bl", 0); err != nil {
		t.Fatal(err)
	}
	if p, err = Find("bl"); err != nil || p.BodyLimit != 0 {
		t.Fatalf("off left %d (%v)", p.BodyLimit, err)
	}
	if err := SetBodyLimit("nosuch", 1); err == nil {
		t.Fatal("an unknown provider was given a limit")
	}
}
