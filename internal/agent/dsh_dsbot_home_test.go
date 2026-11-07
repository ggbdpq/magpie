package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/sessions"
)

// #5624 (BErrybearl): DS Bot's app runs its own DSH with DSH_HOME at its
// data folder — a value the registry holds on Windows, which appdir.Getenv
// follows while magpie sits in the tray. With DSH_HOME naming DS Bot's data
// folder, dsh's row bound there and the serving round wrote magpie's route
// into ~/.ds-bot/profiles/ds-bot/cordis.patch.yml over DS Bot's own
// provider, and its sessions and usage were the harness's. The folder is
// DS Bot's (DsbotHome): dsh keeps ~/.dsh whatever DSH_HOME says when it
// says that, and DS Bot's row keeps the folder.
func TestDshLeavesDsBotsFolder(t *testing.T) {
	home, dshDir, _, _ := dsbotSandbox(t)
	data := filepath.Join(home, ".ds-bot")
	file := dsbotFile(data)
	// the app's own profile, as its first start makes it
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(dshProfileTemplate), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DSH_HOME", data)

	a := dsh(home)
	if a.Dir != dshDir {
		t.Fatalf("dsh bound to DS Bot's data folder: %s", a.Dir)
	}
	if a.Path != filepath.Join(dshDir, "profiles", "web", "cordis.patch.yml") {
		t.Fatalf("dsh's patch list is %s", a.Path)
	}
	if dir := dshHome(); dir != dshDir {
		t.Fatalf("the serving round keeps %s", dir)
	}
	if dir := sessions.DshDir(); dir != dshDir {
		t.Fatalf("sessions read %s", dir)
	}
	// DS Bot's row still owns its folder
	if b := dsbot(home); b.Path != file {
		t.Fatalf("DS Bot's row names %s", b.Path)
	}

	// DS_BOT_HOME moves the folder; DSH_HOME there is still its
	thrown := filepath.Join(home, "throwaway")
	if err := os.MkdirAll(filepath.Join(thrown, "profiles", "ds-bot"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dsbotFile(thrown), []byte(dshProfileTemplate), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DS_BOT_HOME", thrown)
	t.Setenv("DSH_HOME", thrown)
	if dir := dshHome(); dir != dshDir {
		t.Fatalf("the serving round keeps %s with DS_BOT_HOME set", dir)
	}
	if a := dsh(home); a.Dir != dshDir {
		t.Fatalf("dsh bound to %s with DS_BOT_HOME set", a.Dir)
	}
	if b := dsbot(home); b.Path != dsbotFile(thrown) {
		t.Fatalf("DS Bot's row names %s with DS_BOT_HOME set", b.Path)
	}
}
