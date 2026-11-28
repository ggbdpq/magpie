package plugin

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/proc"
)

// A plugin host the writer found dead (a write to its stdin failed) is
// marked failed while its child is still alive. fail only ends the logical
// host: it does not kill the process. Every later stop then sees a closed
// dead and returns without killing either, so one live magpie-bun is left
// behind for each such failure. They accumulate until the container's pids
// limit is hit, when every fork/exec EAGAINs and every request 502s
// (reported in #6464).
func TestFailedHostKillsItsLiveProcess(t *testing.T) {
	cmd := exec.Command("sleep", "61.25")
	tree, err := proc.StartTree(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tree.Kill(); _ = tree.Wait() })
	pid := cmd.Process.Pid
	if !living(pid) {
		t.Fatalf("the child %d did not start", pid)
	}

	hctx, hcancel := context.WithCancel(context.Background())
	t.Cleanup(hcancel)
	h := &host{
		cmd: cmd, in: discardCloser{},
		dead:     make(chan struct{}),
		calls:    map[int64]*call{},
		out:      make(chan writeReq, callLimit()),
		ctrl:     map[int64]*ctrl{},
		ctrlWake: make(chan struct{}, 1),
		ctx:      hctx, cancel: hcancel,
		tree: tree, waited: tree.Wait,
	}

	// the stdin writer found its pipe broken and failed the host
	h.fail(errors.New("writing to the plugin host: broken pipe"))
	// the next retire or EndHosts stops it
	h.stop()

	until := time.Now().Add(5 * time.Second)
	for living(pid) {
		if time.Now().After(until) {
			t.Fatalf("a live magpie-bun (pid %d) is left after the host failed; these accumulate until pids.max and every fork/exec then 502s", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
