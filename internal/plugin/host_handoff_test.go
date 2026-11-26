package plugin

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestHotReloadStopsOldHostAndRunsShutdownHook (#7860): on a hot-reload, the
// replacement plugin host must not start server() before an idle previous host
// has run its optional stop()/shutdown() hook and exited (releasing fixed
// loopback ports and lockfiles cleanly without EADDRINUSE / EEXIST races).
func TestHotReloadStopsOldHostAndRunsShutdownHook(t *testing.T) {
	sandbox(t)
	every := pathsEvery
	pathsEvery = 0
	t.Cleanup(func() { pathsEvery = every })
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	work := t.TempDir()
	lockFile := filepath.Join(work, "svc.lock")
	stopLog := filepath.Join(work, "stop.log")
	dir := filepath.Join(work, "svc-plugin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SVC_PORT", fmt.Sprint(port))
	t.Setenv("SVC_LOCK", lockFile)
	t.Setenv("SVC_LOG", stopLog)

	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"svc-plugin","version":"0.0.1","type":"module","main":"./index.mjs"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	writePlugin := func(ver string, at time.Time) {
		t.Helper()
		src := fmt.Sprintf(`import fs from "node:fs"
export default {
  id: "svc-plugin",
  server: async () => {
    fs.writeFileSync(process.env.SVC_LOCK, %q, { flag: "wx" })
    const srv = Bun.serve({
      hostname: "127.0.0.1",
      port: Number(process.env.SVC_PORT),
      fetch: () => new Response(%q),
    })
    return {
      stop: async () => {
        srv.stop(true)
        try { fs.unlinkSync(process.env.SVC_LOCK) } catch {}
        fs.appendFileSync(process.env.SVC_LOG, "stopped:%s\n")
      },
    }
  },
}
`, ver, ver, ver)
		p := filepath.Join(dir, "index.mjs")
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}

	base := time.Now().Add(-time.Hour)
	writePlugin("v1", base)
	if _, err := Add(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if err := Warm(ctx); err != nil {
		t.Fatal(err)
	}
	fetchBody := func() string {
		t.Helper()
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
		if err != nil {
			t.Fatalf("GET loopback service: %v", err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	if got := fetchBody(); got != "v1" {
		t.Fatalf("initial service answered %q, want v1", got)
	}

	// Edit the local path plugin to trigger hot-reload via checkList -> getLocked.
	writePlugin("v2", base.Add(time.Minute))
	if err := Warm(ctx); err != nil {
		t.Fatal(err)
	}
	if got := fetchBody(); got != "v2" {
		t.Fatalf("after hot-reload service answered %q, want v2 (old host overlapped or EADDRINUSE/EEXIST)", got)
	}
	logBytes, err := os.ReadFile(stopLog)
	if err != nil || !strings.Contains(string(logBytes), "stopped:v1") {
		t.Fatalf("shutdown hook did not run for v1 before reload: err=%v log=%q", err, string(logBytes))
	}
}
