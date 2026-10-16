package gateway

import (
	"net/http"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A key refused for no balance rests half an hour, and every request in
// that time is answered the refusal it gave, without asking (quotaHeld,
// #1772) — so topping the account up changes nothing for up to half an
// hour (#2587). The readings the automatic quota refresh asks anyway — a
// key's windows, its balance card — going through fine say the refusal may
// be stale, so the credit rest is lifted and the next request asks the
// vendor: one that still refuses rests the key as before. A quota rest is
// renewed's to lift, not this.
func TestCreditBackLiftsACreditRest(t *testing.T) {
	forgetRouting()
	s := &Server{}
	fp := provider.KeyID("k1")
	key := candidate{p: provider.Provider{ID: "glm", Key: "k1"}, model: "glm-5.3", rest: "glm"}
	r := s.restAfter(key, 429, http.Header{}, []byte(`{"error":{"code":"1113","message":"余额不足或无可用资源包，请充值"}}`))
	if r.Why != failCredit {
		t.Fatalf("a refusal of no balance failed as %s", r.Why)
	}
	if d := time.Until(r.Until); d <= creditRest-2*time.Minute || d > creditRest {
		t.Fatalf("out of credit rests %v, want about %v", d, creditRest)
	}
	creditBack("glm", fp)
	if r, ok := restOf(key.restKey()); ok {
		t.Fatalf("still resting after its balance was read again: %+v", r)
	}

	// the model's own credit rest is lifted with the key's
	seed := func(k string) {
		restingUntil.Lock()
		restingUntil.m[k] = time.Now().Add(creditRest)
		restingUntil.note[k] = Rest{Why: failCredit, Status: 402, Until: restingUntil.m[k], By: "credit", Key: k}
		restingUntil.Unlock()
	}
	seed("glm/glm-5.3-paid") // one model of the key rests alone
	seed("glm#" + fp + "/glm-5.3-paid")
	creditBack("glm", fp)
	for _, k := range []string{"glm/glm-5.3-paid", "glm#" + fp + "/glm-5.3-paid"} {
		if _, ok := restOf(k); ok {
			t.Fatalf("%s still rests out of credit after the key's balance was read again", k)
		}
	}

	// another key of the provider, and another provider's key, keep theirs
	seed("glm#" + provider.KeyID("k2"))
	seed("other")
	creditBack("glm", fp)
	for _, k := range []string{"glm#" + provider.KeyID("k2"), "other"} {
		if _, ok := restOf(k); !ok {
			t.Fatalf("%s: another key's credit rest was lifted", k)
		}
	}

	// a quota rest is renewed's to lift, and creditBack leaves it
	restingUntil.Lock()
	restingUntil.m["glm"] = time.Now().Add(quotaRest)
	restingUntil.note["glm"] = Rest{Why: failQuota, Status: 429, Until: restingUntil.m["glm"], By: "quota", Key: "glm"}
	restingUntil.Unlock()
	creditBack("glm", fp)
	if _, ok := restOf("glm"); !ok {
		t.Fatal("a quota rest was lifted by a balance read")
	}
}
