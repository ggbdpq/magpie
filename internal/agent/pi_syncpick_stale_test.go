package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/edit"
)

// #2373: a pick of a magpie ref the catalog doesn't resolve yet (a channel
// just signed in to, its models not fetched) still wrote settings.json's
// defaultModel, and skipped the models.json rewrite: Pi started on a model
// its magpie group didn't list. The prefix says the pick is magpie's; the
// provider block is rewritten whatever the catalog resolves.
func TestPiPickUnresolvedRefRewritesModels(t *testing.T) {
	home := syncHome(t)
	a := pi(home)
	if err := a.Field("model").Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	models := filepath.Join(home, ".pi", "agent", "models.json")
	before, err := os.Stat(models)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond) // the rewrite is not a same-instant one
	if err := a.Field("model").Set("magpie/trae/kimi-k3-max"); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(home, ".pi", "agent", "settings.json")
	if m, _ := edit.GetJSON(settings, "defaultModel"); m != "trae/kimi-k3-max" {
		t.Fatalf("settings.json:\n%s", readFile(settings))
	}
	after, err := os.Stat(models)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().After(before.ModTime()) {
		t.Fatalf("models.json was not rewritten though settings.json names the model:\n%s", readFile(models))
	}
}
