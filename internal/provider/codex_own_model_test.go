package provider

import (
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// def324 (#6898): two Codex accounts, both fetched. The vendor lists
// codex-auto-review hidden, so the cached list of either account holds
// only the big models — and an account whose list says nothing of it read
// as not serving it (unlisted), while the account whose list was never
// fetched went first. A guardian call then landed on the account at its
// 5-hour cap and 429'd, though the other account was at 0% and answered
// 200 pinned. ChatGPT serves the guardian's model to every plan: an
// account's list saying nothing of it is not an account lacking it.
func TestCodexListsTheGuardianModel(t *testing.T) {
	list := parseCodexModels([]byte(`{"models":[
		{"slug":"gpt-6.1","display_name":"GPT-6.1","visibility":"hide","context_window":400000},
		{"slug":"codex-auto-review","display_name":"Auto review","visibility":"hide","context_window":272000},
		{"slug":"gpt-6.1-sol","display_name":"Sol","context_window":400000}
	]}`))
	if len(list) == 0 {
		t.Fatal("the fetch parsed to nothing")
	}
	if slices.ContainsFunc(list, func(m catalog.Model) bool { return m.ID == "codex-auto-review" }) {
		t.Fatal("the hidden guardian model is in the picker list")
	}
	if err := catalog.SaveLive(accountModels("codex", "a@example.com"), "", list); err != nil {
		t.Fatal(err)
	}
	a := &Account{Agent: "codex", User: "a@example.com"}
	if !a.Lists("codex-auto-review") {
		t.Error("the account read as not serving the guardian's model, and the route held every approval to whichever account's list was never fetched")
	}
	// the models the list does name keep their verdicts
	if !a.Lists("gpt-6.1-sol") {
		t.Error("a listed model read as not served")
	}
	if a.Lists("gpt-6.1") {
		t.Error("a hidden non-guardian model read as served")
	}
}
