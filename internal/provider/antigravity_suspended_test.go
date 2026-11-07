package provider

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// antigravitySuspendedLoad is Google's answer to loadCodeAssist for an
// Antigravity account it suspended, as #5478's reporter (cuic19053-hue)
// showed it: a 403 PERMISSION_DENIED, reason TOS_VIOLATION, naming the
// appeal — the same words the gateway reads a request's refusal by.
const antigravitySuspendedLoad = `{"error":{"code":403,"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","domain":"cloudcode-pa.googleapis.com","metadata":{"appeal_url":"https://forms.gle/hGzM9MEUv2azZsrb9","appeal_url_link_text":"Submit Appeal","uiMessage":"true"},"reason":"TOS_VIOLATION"}],"message":"This service has been disabled in this account for violation of Terms of Service. Please submit an appeal to continue using this product.","status":"PERMISSION_DENIED"}}`

// tosRefusal is loadCodeAssist answering as it does for a suspended
// account, whatever it was asked.
func tosRefusal(map[string]any) (int, string) {
	return 403, antigravitySuspendedLoad
}

// fakeDiscoveryGateway is the gateway's side of the discovery hooks, as a
// test stands in for it: a refusal it is told is rested, and an ask is
// held with the vendor's words while the rest stands, the way the gateway
// rests one of its own refusals and answers discovery's ask from it.
type fakeDiscoveryGateway struct {
	mu     sync.Mutex
	told   []string
	rested map[string]string
}

func newFakeDiscoveryGateway(t *testing.T) *fakeDiscoveryGateway {
	t.Helper()
	g := &fakeDiscoveryGateway{rested: map[string]string{}}
	DiscoveryRefused = func(agent, user string, status int, said string) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.told = append(g.told, fmt.Sprintf("%s %s %d", agent, user, status))
		g.rested[agent+"\x00"+user] = said
	}
	DiscoveryHeld = func(agent, user string) string {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.rested[agent+"\x00"+user]
	}
	t.Cleanup(func() { DiscoveryRefused, DiscoveryHeld = nil, nil })
	return g
}

// toldTimes is how often the gateway was told of agent, user's refusal.
func (g *fakeDiscoveryGateway) toldTimes(agent, user string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, t := range g.told {
		if strings.HasPrefix(t, agent+" "+user+" ") {
			n++
		}
	}
	return n
}

// lift takes the rest of the account back, as the Routing page's Unrest
// does when an appeal went through.
func (g *fakeDiscoveryGateway) lift(agent, user string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.rested, agent+"\x00"+user)
}

// A suspended account's discovery — its model list, asked again by the
// magpie serving the gateway once it is an hour old, its tiers with it —
// runs outside the gateway's request path, and Google's 403 reached it
// unseen: the account was asked, and refused, on every start and every
// hour for good (#5478, the case asked for in plugins#784). The refusal is
// told to the gateway, which rests the account restricted as it rests one
// of its own refusals; Google is sent nothing more for the account — its
// tiers, its models — while the rest stands, and once it is lifted the
// account is asked again.
func TestDiscoveryRestsASuspendedAccount(t *testing.T) {
	isolate(t)
	f := &fakeGoogle{load: proLoad, modelsFor: tierModels("g1-pro-tier")}
	googleSandbox(t, f)
	auth := googleAuth{AccessToken: "tok", RefreshToken: "rt-susp", Expiry: time.Now().Add(time.Hour).UnixMilli()}
	if err := addGoogleLogin("antigravity", "ag@example.com", "", auth); err != nil {
		t.Fatal(err)
	}
	p, ok := googleAccountOf("antigravity")
	if !ok {
		t.Fatal("no antigravity account")
	}
	ctx := context.Background()
	if _, err := p.Fetch(ctx); err != nil {
		t.Fatal(err)
	}
	g := newFakeDiscoveryGateway(t)
	loads, fetches := len(f.loads), len(f.fetches)

	// Google suspends the account, as the reporter's log has it:
	// loadCodeAssist refuses, and a start of magpie has no project kept
	f.mu.Lock()
	f.loadFor = tosRefusal
	f.mu.Unlock()
	resetGoogleState()

	if got := relistAccounts(ctx, time.Now().Add(accountListAge+time.Minute)); !slices.Equal(got, []string{"antigravity"}) {
		t.Fatalf("the suspended account wasn't asked for: %v", got)
	}
	if n := g.toldTimes("antigravity", "ag@example.com"); n != 1 {
		t.Fatalf("the refusal was told to the gateway %d times: %v", n, g.told)
	}
	if said := DiscoveryHeld("antigravity", "ag@example.com"); !strings.Contains(said, "violation of Terms of Service") {
		t.Fatalf("the gateway wasn't told Google's words: %q", said)
	}
	if n := len(f.loads) - loads; n != 1 {
		t.Fatalf("loadCodeAssist was asked %d times, want once", n)
	}
	if n := len(f.fetches) - fetches; n != 0 {
		t.Fatalf("the suspended account's models were asked for %d times after loadCodeAssist refused it", n)
	}

	// the rest standing, the next look asks Google nothing at all
	l0, c0 := len(f.loads), len(f.fetches)
	if got := relistAccounts(ctx, time.Now().Add(2*accountListAge+2*time.Minute)); len(got) != 0 {
		t.Fatalf("the resting account was asked for: %v", got)
	}
	if len(f.loads) != l0 || len(f.fetches) != c0 {
		t.Fatalf("Google was asked while the account rested: %d loadCodeAssist, %d fetchAvailableModels more", len(f.loads)-l0, len(f.fetches)-c0)
	}

	// the appeal went through; the rest lifted, the account is asked again
	g.lift("antigravity", "ag@example.com")
	f.mu.Lock()
	f.loadFor = nil
	f.mu.Unlock()
	if got := relistAccounts(ctx, time.Now().Add(3*accountListAge+3*time.Minute)); !slices.Equal(got, []string{"antigravity"}) {
		t.Fatalf("the account wasn't asked for again once lifted: %v", got)
	}
	if len(f.fetches) == c0 {
		t.Fatal("its models weren't asked for again")
	}

	// the project kept and the tiers an hour old, the hourly tiers ask
	// reads the refusal the same way, and the pass stops before the models
	f.mu.Lock()
	f.loadFor = tosRefusal
	f.mu.Unlock()
	googleState.Lock()
	for k, q := range googleState.projects {
		q.at = time.Now().Add(-2 * time.Hour)
		googleState.projects[k] = q
	}
	googleState.Unlock()
	l1, c1 := len(f.loads), len(f.fetches)
	if got := relistAccounts(ctx, time.Now().Add(4*accountListAge+4*time.Minute)); !slices.Equal(got, []string{"antigravity"}) {
		t.Fatalf("the suspended account wasn't asked for: %v", got)
	}
	if n := g.toldTimes("antigravity", "ag@example.com"); n != 2 {
		t.Fatalf("the tiers ask's refusal was told %d times: %v", n, g.told)
	}
	if n := len(f.loads) - l1; n != 1 {
		t.Fatalf("loadCodeAssist was asked %d times, want the tiers' one ask", n)
	}
	if n := len(f.fetches) - c1; n != 0 {
		t.Fatalf("the models were asked for %d times after the tiers said the account is suspended", n)
	}
}

// Only Google's own suspension is read so: loadCodeAssist refused for
// another reason tells the gateway nothing, and the account's list is
// fetched as it always was.
func TestDiscoveryTellsOnlyGooglesSuspension(t *testing.T) {
	isolate(t)
	f := &fakeGoogle{load: proLoad, modelsFor: tierModels("g1-pro-tier")}
	googleSandbox(t, f)
	auth := googleAuth{AccessToken: "tok", RefreshToken: "rt-tos", Expiry: time.Now().Add(time.Hour).UnixMilli()}
	if err := addGoogleLogin("antigravity", "tos@example.com", "", auth); err != nil {
		t.Fatal(err)
	}
	p, ok := googleAccountOf("antigravity")
	if !ok {
		t.Fatal("no antigravity account")
	}
	ctx := context.Background()
	if _, err := p.Fetch(ctx); err != nil {
		t.Fatal(err)
	}
	g := newFakeDiscoveryGateway(t)

	// another 403 of Google's, which says nothing of the account's terms:
	// a plain PERMISSION_DENIED
	f.mu.Lock()
	resetGoogleState()
	f.loadFor = func(map[string]any) (int, string) {
		return 403, `{"error":{"code":403,"message":"The caller does not have permission","status":"PERMISSION_DENIED"}}`
	}
	f.mu.Unlock()
	if got := relistAccounts(ctx, time.Now().Add(accountListAge+time.Minute)); !slices.Equal(got, []string{"antigravity"}) {
		t.Fatalf("the account wasn't asked for: %v", got)
	}
	if n := g.toldTimes("antigravity", "tos@example.com"); n != 0 {
		t.Fatalf("a 403 that isn't the suspension was told to the gateway: %v", g.told)
	}
	if said := DiscoveryHeld("antigravity", "tos@example.com"); said != "" {
		t.Fatalf("the account is held with %q", said)
	}
}
