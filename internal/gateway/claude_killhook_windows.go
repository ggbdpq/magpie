//go:build windows

package gateway

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"syscall"

	"github.com/yetone/magpie/internal/proc"
)

// hookCommand is KillHookEnv's line for cmd.exe. A line with a quote in
// it cannot go as an argument: Go quotes such an argument `\"`, which
// cmd.exe reads back as a backslash and a quote, and with more than two
// quotes on the line cmd.exe drops the first and the last
// (proc/batch_windows.go), so `echo x>"C:\path"` ended in a path of
// `\"C:\path` — 文件名、目录名或卷标语法不正确. The line goes as a raw
// command line instead (SysProcAttr.CmdLine, the way proc's batch builds
// one): `"cmd.exe" /d /s /c "<line>"`, where /s has cmd.exe strip only
// the first and the last quote, the two this line adds — the user's
// line, its quotes among them, is read as written. /d skips AutoRun, so
// a machine's own cmd.exe startup runs nothing beside the user's line. A
// line break is refused, as it would end the command (errBatchArg's
// reason).
func hookCommand(ctx context.Context, line string) (*exec.Cmd, error) {
	if strings.ContainsAny(line, "\r\n") {
		return nil, errHookLine
	}
	cmd := proc.CommandContext(ctx, "cmd")
	if cmd.SysProcAttr == nil { // hide's CREATE_NO_WINDOW is kept
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CmdLine = `"` + cmd.Path + `" /d /s /c "` + line + `"`
	return cmd, nil
}

var errHookLine = errors.New("the line has a line break in it")
