//go:build !windows

package gateway

import (
	"context"
	"os/exec"

	"github.com/yetone/magpie/internal/proc"
)

// hookCommand is KillHookEnv's line for /bin/sh, as written.
func hookCommand(ctx context.Context, line string) (*exec.Cmd, error) {
	return proc.CommandContext(ctx, "/bin/sh", "-c", line), nil
}
