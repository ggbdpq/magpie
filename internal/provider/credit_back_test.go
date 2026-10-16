package provider

import (
	"testing"
	"time"
)

// A card read of a key — its balance, or a plan's windows — that went
// through tells OnCreditBack's hooks, so the gateway can lift the credit
// rest the key was left resting under when it had none (#2587): the vendor
// answering a read says its old refusal may be stale.
func TestCardReadsTellCreditBack(t *testing.T) {
	creditBackHooks.Lock()
	old := creditBackHooks.fs
	creditBackHooks.fs = nil
	creditBackHooks.Unlock()
	defer func() {
		creditBackHooks.Lock()
		creditBackHooks.fs = old
		creditBackHooks.Unlock()
	}()

	got := make(chan [2]string, 4)
	OnCreditBack(func(providerID, fingerprint string) {
		got <- [2]string{providerID, fingerprint}
	})

	p := Provider{ID: "glm", Key: "k1"}
	noteKeyAllowance(p, nil, time.Now())

	select {
	case id := <-got:
		if id[0] != "glm" || id[1] != KeyID("k1") {
			t.Fatalf("told %q %q, want glm %q", id[0], id[1], KeyID("k1"))
		}
	case <-time.After(time.Second):
		t.Fatal("a card read of a key told nobody")
	}
}
