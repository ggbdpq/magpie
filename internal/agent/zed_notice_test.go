package agent

import (
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// zfs-zed (the ZFS Event Daemon, /usr/sbin/zed -F) is not the editor: its
// command line matches Zed's own pgrep pattern, and a machine with zfsutils
// installed got told to restart an editor that wasn't running (#5023).
// listRunningNow is fed the daemon's line and the editor's; the notice's
// check must take the editor and drop the daemon.
func TestZedRunningIgnoresTheZFSDaemon(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot be asked what runs (Running answers yes)")
	}
	wasRunning := Running
	t.Cleanup(func() { Running = wasRunning })
	Running = func(patterns ...string) bool { return true }
	// the notice through its own seam: a machine whose only zed match is
	// the daemon gets no restart notice; an editor's line says so.
	was := Running
	t.Cleanup(func() { Running = was })
	Running = func(patterns ...string) bool { return true }
	home := syncHome(t)
	a := zedAt(filepath.Join(home, ".config", "zed"))
	writeFile(t, a.Path, `{"agent": {"default_model": {"provider": "magpie", "model": "magpie/relay/glm-4.6"}}, "language_models": {"openai_compatible": {"magpie": {"api_url": "http://127.0.0.1:1/v1", "available_models": [{"name": "relay/glm-4.6"}]}}}}`)
	for _, c := range []struct {
		cmdline string
		want    bool
	}{
		{"/usr/sbin/zed -F", false},
		{"zed", true},
	} {
		ps := zedProcessNames()
		lately.put(ps[0], time.Now(), []runningProc{{0, c.cmdline}})
		if got := a.Notice() != ""; got != c.want {
			t.Errorf("notice with %q running = %v, want %v", c.cmdline, got, c.want)
		}
		lately.Lock()
		delete(lately.by, ps[0])
		lately.Unlock()
	}
}
