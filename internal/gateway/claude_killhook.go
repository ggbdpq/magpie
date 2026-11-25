package gateway

import (
	"context"
	"log"
	"os"
	"time"
)

// RunIDEnv is a run's tag in its Claude Code's environment, named after
// the run's own temp folder (magpie-claude-<n>), which the run's
// --mcp-config carries on its command line already. A `claude` on PATH
// that is a wrapper launching the real one across an exec boundary —
// `docker exec` into a container, say — is ended with its tree here, but
// the Claude Code it exec'd there is another pid namespace's: it
// reparents to the container's init and survives every kill magpie
// signals, past what claudeRoom and the run tracker count (#7747). The
// tag is what a wrapper can carry there (`docker exec -e
// MAGPIE_RUN_ID=<cell>`), and what the kill hook is told of the run.
const RunIDEnv = "MAGPIE_RUN_ID"

// KillHookEnv names a command run once a run has ended, RunIDEnv in its
// environment naming the run: the one way to end a Claude Code that
// lives where magpie's process-group kill cannot reach (#7747). The
// command is the user's, for their wrapper — a cell's keeper, say, ends
// the exec'd one by the tag its wrapper carried in:
//
//	docker exec <cell> sh -c 'grep -l "MAGPIE_RUN_ID=$MAGPIE_RUN_ID" /proc/[0-9]*/environ | cut -d/ -f3 | xargs -r kill'
//
// with `$MAGPIE_RUN_ID` the hook's own environment's, expanded before
// docker sees it. It runs beside the teardown, best effort: what it
// says is logged, and nothing waits for it but its own bound
// (killHookFor).
const KillHookEnv = "MAGPIE_CLAUDE_KILL"

// killHookFor is how long the kill hook may take: a hung one — a docker
// daemon gone slow, say — must not hold the runs ended beside it.
var killHookFor = 15 * time.Second

// runKillHook runs KillHookEnv's command with RunIDEnv set to runID, the
// tag of a run just ended. Said, or a run without one, it does nothing.
func runKillHook(runID string) {
	line := os.Getenv(KillHookEnv)
	if line == "" || runID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), killHookFor)
	defer cancel()
	cmd, err := hookCommand(ctx, line)
	if err != nil {
		log.Printf("claude: %s refused (%v)", KillHookEnv, logSafe(err.Error()))
		return
	}
	cmd.Env = append(os.Environ(), RunIDEnv+"="+runID)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("claude: %s on run %s failed (%v): %s", KillHookEnv, runID, logSafe(err.Error()), logSafe(string(out)))
	}
}
