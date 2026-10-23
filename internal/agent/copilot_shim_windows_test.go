//go:build windows

package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/testenv"
)

// The shim reaches Detected on Windows through FindTool too, which asks the
// registry's PATH a running magpie doesn't have: the editor's update put it
// there after magpie started (#3269, as #839 was for Claude Code).
func TestCopilotLauncherOnTheRegistrysPATHIsNotTheCLI(t *testing.T) {
	if os.Getenv(testenv.Marker) == "" {
		t.Skip("not in a sandbox: the registry's folders would be the machine's own")
	}
	dirs := proc.SystemDirs(proc.LoginPath()...)
	if len(dirs) == 0 {
		t.Skip("no PATH in this machine's registry")
	}
	shim := filepath.Join(dirs[len(dirs)-1], "copilot-shim", "copilot.exe")
	writeAt(t, shim, "vscode-copilot-launcher")
	t.Cleanup(func() { os.Remove(shim) })
	if copilot(t.TempDir()).Detected() {
		t.Error("a copilot launcher on the registry's PATH is taken for the Copilot CLI")
	}
}
