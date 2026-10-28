package provider

import (
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// Codex is offered Ultrafast on a ChatGPT account's GPT model when one of
// Codex's accounts lists it on the model, whichever account Codex itself is
// signed in to: Codex sends only a tier its list names, and the gateway
// sends an ultrafast ask to an account that offers it. Not on a model no
// account offers it on, and not on a group, whose other members would be
// sent ultrafast as it is.
func TestCodexTiersOfferUltrafastAnAccountHas(t *testing.T) {
	acct := Provider{ID: "codex", Account: &Account{Agent: "codex"}}
	relay := Provider{ID: "company"}
	ultra := map[string]bool{"gpt-6.1-sol": true}
	members := func(string) []Member {
		return []Member{{Provider: relay, Model: "gpt-6.1-sol"}, {Provider: acct, Model: "gpt-6.1-sol"}}
	}
	tiers := func(e Entry, ultra map[string]bool) catalog.Model {
		var m catalog.Model
		CodexTiers(&m, e, members, ultra)
		return m
	}
	if m := tiers(Entry{ID: "codex/gpt-6.1-sol", Model: "gpt-6.1-sol", Provider: acct}, ultra); !slices.Equal(m.Tiers, []string{"priority", "ultrafast"}) {
		t.Errorf("an account's model offered %v, want priority and ultrafast", m.Tiers)
	}
	if m := tiers(Entry{ID: "codex/gpt-5.5", Model: "gpt-5.5", Provider: acct}, ultra); slices.Contains(m.Tiers, "ultrafast") {
		t.Errorf("a model no account offers Ultrafast on offered %v", m.Tiers)
	}
	if m := tiers(Entry{ID: "codex/gpt-6.1-sol", Model: "gpt-6.1-sol", Provider: acct}, nil); slices.Contains(m.Tiers, "ultrafast") {
		t.Errorf("no accounts read, offered %v", m.Tiers)
	}
	if m := tiers(Entry{ID: GroupPrefix + "r", Model: "gpt-6.1-sol", Provider: acct, Group: "r"}, ultra); slices.Contains(m.Tiers, "ultrafast") || !m.Fast {
		t.Errorf("a group offered %v (fast %v), want Fast alone", m.Tiers, m.Fast)
	}
	if m := tiers(Entry{ID: "company/gpt-6.1-sol", Model: "gpt-6.1-sol", Provider: relay}, ultra); slices.Contains(m.Tiers, "ultrafast") {
		t.Errorf("a relay's model offered %v", m.Tiers)
	}
}
