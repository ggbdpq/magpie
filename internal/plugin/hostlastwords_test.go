package plugin

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// bunCrashBanner is what Bun said when the plugin host died on it, from
// #7967's docker logs (LeoGZ: OpenCode Go, 73 crashes in 24h, Bun 1.4.2).
const bunCrashBanner = `panic(main thread): abort() called
oh no: Bun has crashed. This indicates a bug in Bun, not your code.
Linux Kernel v3.10.0 | glibc v2.41
`

// crashingBun is a bun that says Bun's own crash banner to stderr and dies
// SIGABRT, as Bun 1.4.2 did starting the host on that kernel.
func crashingBun(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the bun is a shell script")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncat >&2 <<'OUT'\n" + bunCrashBanner + "OUT\nkill -s ABRT $$\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// A host whose bun dies starting it reports what the bun said, not just
// "signal: aborted": the banner names the cause (Bun's own crash note, the
// kernel line), and it is otherwise in magpie's log alone, where the
// reporter of #7967 only found it after a day of 73 crashes.
func TestHostDiedSaysWhatBunSaid(t *testing.T) {
	bunHome(t, BunVersion, time.Now())
	t.Cleanup(Settle)
	crashingBun(t, bunExeOf(BunVersion))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err := Providers(ctx)
	if err == nil {
		t.Fatal("Providers on a bun that crashed starting the host: no error")
	}
	if !strings.Contains(err.Error(), "the plugin host quit") {
		t.Fatalf("Providers on a crashed host died another way: %v", err)
	}
	if !strings.Contains(err.Error(), "Bun has crashed") || !strings.Contains(err.Error(), "Kernel v3.10.0") {
		t.Fatalf("the error of a host whose bun crashed says nothing of it: %v", err)
	}
}
