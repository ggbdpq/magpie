package browserext

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// When Chrome freezes the MV3 service worker (or the machine sleeps), the
// WebSocket stays at readyState 1 without firing onclose. Once the 30s
// alarm wakes the worker past the stale threshold with nothing heard from
// the gateway, connect drops the half-open socket and opens a new one; a
// late onclose from the dropped socket never clobbers the live one (#7177).
func TestAlarmReplacesHalfOpenSocket(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH")
	}
	dir, err := filepath.Abs("ext")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, node, "--input-type=module", "-e", halfOpenReconnectJS, dir).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

const halfOpenReconnectJS = `
import { pathToFileURL } from "node:url";
import path from "node:path";

const dir = process.argv[1];
const fail = (m) => { console.error(m); process.exit(1); };

let now = 1_700_000_000_000;
Date.now = () => now;

let onAlarm = null;
let onStorageChanged = null;
let lastStatus = null;
let storedGateway = "ws://127.0.0.1:3425";

Object.defineProperty(globalThis, "navigator", { value: { userAgent: "Chrome/157.0" }, configurable: true });
globalThis.chrome = {
  runtime: {
    getManifest: () => ({ version: "0.1.10" }),
    onStartup: { addListener() {} },
    onInstalled: { addListener() {} },
    onMessage: { addListener() {} },
  },
  storage: {
    local: { get: async (k) => (k === "gateway" ? { gateway: storedGateway } : {}) },
    session: {
      get: async () => ({}),
      set: async (v) => { if (v && v.status) lastStatus = v.status; },
    },
    onChanged: { addListener(f) { onStorageChanged = f; } },
  },
  action: {
    setBadgeText: async () => {},
    setBadgeBackgroundColor: async () => {},
  },
  alarms: {
    create() {},
    onAlarm: { addListener(f) { onAlarm = f; } },
  },
  tabs: { onRemoved: { addListener() {} } },
  debugger: {
    onEvent: { addListener() {} },
    onDetach: { addListener() {} },
  },
};

const sockets = [];
class FakeWebSocket {
  constructor(url) {
    this.url = url;
    this.readyState = 0; // CONNECTING
    this.sent = [];
    this.closed = false;
    sockets.push(this);
  }
  send(data) { this.sent.push(data); }
  close() {
    this.closed = true;
    // Half-open simulation: do not automatically fire onclose here so tests
    // can control whether/when a late onclose arrives.
  }
}
globalThis.WebSocket = FakeWebSocket;

const flush = async () => {
  for (let i = 0; i < 6; i++) await new Promise((r) => setImmediate(r));
};

await import(pathToFileURL(path.join(dir, "background.js")).href);
await flush();

if (sockets.length !== 1) fail("expected 1 initial socket, got " + sockets.length);
const s1 = sockets[0];
s1.readyState = 1;
s1.onopen();
if (!lastStatus?.connected) fail("expected connected status after s1 open");

// Normal keepalive: pong heard 10s ago, alarm must keep s1 open.
now += 20_000;
s1.onmessage({ data: JSON.stringify({ type: "pong" }) });
now += 10_000;
onAlarm();
await flush();
if (sockets.length !== 1 || s1.closed) fail("healthy socket was replaced prematurely: count=" + sockets.length + " closed=" + s1.closed);

// Service worker freeze: 50s pass with no message heard, readyState stays 1,
// onclose never fires. The 30s alarm must close s1 and open s2.
now += 50_000;
onAlarm();
await flush();
if (sockets.length !== 2) fail("alarm did not reconnect half-open socket: sockets=" + sockets.length);
if (!s1.closed) fail("half-open socket s1 was not closed before reconnecting");

const s2 = sockets[1];
s2.readyState = 1;
s2.onopen();
if (!lastStatus?.connected) fail("expected connected status after s2 open");

// A late onclose from the dropped s1 must not mark s2 disconnected or open s3.
s1.onclose();
await flush();
if (!lastStatus?.connected) fail("late onclose from dropped s1 clobbered s2 connected status");
if (sockets.length !== 2) fail("late onclose from dropped s1 opened spurious socket: " + sockets.length);

// Gateway URL change while s2 is half-open (close() never fires onclose)
// must still connect to the new gateway.
storedGateway = "ws://127.0.0.1:4425";
onStorageChanged({ gateway: { newValue: storedGateway } }, "local");
await flush();
if (sockets.length !== 3 || !sockets[2].url.startsWith("ws://127.0.0.1:4425/")) {
  fail("storage change on half-open socket did not reconnect: sockets=" + sockets.length);
}

// Stuck CONNECTING (readyState 0) past stale threshold must also be replaced.
const s3 = sockets[2];
now += 50_000;
onAlarm();
await flush();
if (sockets.length !== 4 || !s3.closed) {
  fail("alarm did not replace stuck CONNECTING socket: sockets=" + sockets.length + " s3.closed=" + s3.closed);
}

process.exit(0);
`
