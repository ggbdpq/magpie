package gateway

import "testing"

// #6313: an upstream that numbers its tool calls with ids whose raw form
// already carries a NUL (e.g. "call_AAA…\x00…b4") has them salted on the way
// to a Messages client, so Claude Code's one id Set keeps the calls apart.
// When a routing group replays that conversation to the upstream as a
// non-streaming request, saltedCallIDsBack must give the upstream its own
// ids back, whole. unsaltCallID cut at the FIRST NUL — the separator saltCallID
// appends — so ids that differed only after that NUL all collapsed to the
// same prefix and the upstream rejected the request with
// "tool_use ids must be unique".
func TestUnsaltCallIDKeepsEmbeddedNUL(t *testing.T) {
	b4 := "call_AAAAAAAAAAAAAAAAAAAAAAAA\x000000000000000000000000b4"
	b5 := "call_AAAAAAAAAAAAAAAAAAAAAAAA\x000000000000000000000000b5"
	d4, ok4 := unsaltCallID(saltCallID(b4, "salt-4"))
	d5, ok5 := unsaltCallID(saltCallID(b5, "salt-5"))
	if !ok4 || !ok5 {
		t.Fatalf("a salted NUL id did not decode: %q/%v %q/%v", d4, ok4, d5, ok5)
	}
	if d4 == d5 {
		t.Fatalf("tool_use ids must be unique: two upstream ids that differ after the NUL collapsed to %q", d4)
	}
	if d4 != b4 || d5 != b5 {
		t.Fatalf("unsaltCallID lost the NUL-separated tail: got %q and %q, want %q and %q — the upstream then rejects the request with tool_use ids must be unique", d4, d5, b4, b5)
	}
}
