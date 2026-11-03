package gui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/update"
)

// A version downloaded and waiting for a restart gives way to a newer one
// out since, so the restart goes straight to the latest.
func TestUpdateStagedGivesWayToNewer(t *testing.T) {
	body := []byte("new magpie")
	sum := sha256.Sum256(body)
	var latest atomic.Value
	latest.Store("0.1.11")
	var downloads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/asset" {
			downloads.Add(1)
			w.Write(body)
			return
		}
		json.NewEncoder(w).Encode(update.Release{Version: latest.Load().(string), Assets: map[string]update.Asset{
			update.BinaryAsset(): {URL: "http://" + r.Host + "/asset", SHA256: hex.EncodeToString(sum[:])},
		}})
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_UPDATE_FEED", srv.URL)
	old := Version
	Version = "0.1.10"
	defer func() { Version = old }()
	exe, err := update.Executable()
	if err != nil {
		t.Fatal(err)
	}
	u := &updater{exe: exe}
	u.self, _ = os.Stat(exe)
	defer os.Remove(exe + ".new")

	u.check()
	if j := u.json(); j.State != "ready" || j.Latest != "0.1.11" || downloads.Load() != 1 {
		t.Fatalf("%+v, %d downloads", j, downloads.Load())
	}
	// nothing newer: it stays as it is, not downloaded again
	u.check()
	if j := u.json(); j.State != "ready" || j.Latest != "0.1.11" || downloads.Load() != 1 {
		t.Fatalf("%+v, %d downloads", j, downloads.Load())
	}
	// the feed down: still ready to restart into what it has
	t.Setenv("MAGPIE_UPDATE_FEED", "http://127.0.0.1:1/")
	u.check()
	if j := u.json(); j.State != "ready" || j.Latest != "0.1.11" {
		t.Fatalf("%+v", j)
	}
	t.Setenv("MAGPIE_UPDATE_FEED", srv.URL)
	// 0.1.13 out: that is the one it restarts into
	latest.Store("0.1.13")
	u.recheck(false)
	for u.json().State == "downloading" || u.json().State == "checking" {
		time.Sleep(10 * time.Millisecond)
	}
	if j := u.json(); j.State != "ready" || j.Latest != "0.1.13" || downloads.Load() != 2 || u.staged == "" {
		t.Fatalf("%+v, %d downloads", j, downloads.Load())
	}
}

// The service (`magpie daemon install`) runs magpie from the very binary
// an update replaces: a restart into the update restarts the service
// into the new one (#4335). An update another magpie put in (`magpie
// update`, in a terminal) isn't this one's to act on, that command
// having restarted the service already; and magpie web reexecs itself in
// its own place, as the service when it is one.
func TestUpdateRestartsTheService(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	exe := filepath.Join(t.TempDir(), "magpie")
	os.WriteFile(exe, []byte("running magpie"), 0o755)

	var restarts atomic.Int32
	wasRestart := restartReplaced
	restartReplaced = func() (bool, error) { restarts.Add(1); return true, nil }
	defer func() { restartReplaced = wasRestart }()
	var relaunched atomic.Bool
	wasRelaunch := relaunchBinary
	relaunchBinary = func(string, string, bool, string) error {
		relaunched.Store(true)
		return nil
	}
	defer func() { relaunchBinary = wasRelaunch }()

	u := &updater{exe: exe, latest: &update.Release{Version: "0.1.11"}}
	u.self, _ = os.Stat(exe)
	u.staged = exe + ".new"
	os.WriteFile(u.staged, []byte("new magpie"), 0o755)
	defer os.Remove(u.staged)
	o := updates
	updates = u
	defer func() { updates = o }()

	// a swap this one makes: the service restarted with it
	if !restartToUpdate(false, true, "") {
		t.Fatal("restart to update failed")
	}
	if !relaunched.Load() {
		t.Fatal("the new version never opened")
	}
	if restarts.Load() != 1 {
		t.Fatalf("the service was restarted %d times", restarts.Load())
	}

	// an update another magpie put in: the service is not restarted here
	os.WriteFile(exe, []byte("another magpie's update, in already"), 0o755)
	u.staged, u.state = "", "ready"
	if !restartInto(false, true, "", true) {
		t.Fatal("a restart beside another magpie's update failed")
	}
	if restarts.Load() != 1 {
		t.Fatalf("the service was restarted %d times for another magpie's update", restarts.Load())
	}

	// the web reexecs itself in its own place, as the service when it is
	// one: no restart of it from here either
	os.WriteFile(exe, []byte("running magpie"), 0o755)
	u.self, _ = os.Stat(exe)
	u.staged, u.state = exe+".new", "ready"
	os.WriteFile(u.staged, []byte("new magpie"), 0o755)
	wasReexec := webReexec.Load()
	defer webReexec.Store(wasReexec)
	if !restartInto(true, false, "", true) {
		t.Fatal("a web restart to update failed")
	}
	if restarts.Load() != 1 {
		t.Fatalf("the service was restarted %d times beside the web's own reexec", restarts.Load())
	}
}
