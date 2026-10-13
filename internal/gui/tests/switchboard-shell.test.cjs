// Run with `node --test internal/gui/tests/switchboard-shell.test.cjs`; no
// browser is needed. shell.js's pure parts are the seam: layoutOf decides
// which layout a load gets (the switchboard is opt-in until it is ready;
// the tray panel keeps its tabs until its own), and redirectOf says where
// an old link lands in it — the classic pages keep their addresses, and a
// reload of the switchboard comes back to it.
const assert = require("node:assert/strict");
const { test } = require("node:test");
const shell = require("../assets/shell.js");

test("layoutOf: classic unless asked for, the query wins over the kept pick", () => {
  assert.equal(shell.layoutOf("", "", "window"), "classic");
  assert.equal(shell.layoutOf("", null, "window"), "classic");
  assert.equal(shell.layoutOf("", "garbage", "window"), "classic");
  assert.equal(shell.layoutOf("", "switchboard", "window"), "switchboard");
  assert.equal(shell.layoutOf("?layout=switchboard", "", "window"), "switchboard");
  assert.equal(shell.layoutOf("?layout=switchboard", "classic", "window"), "switchboard");
  assert.equal(shell.layoutOf("?layout=classic", "switchboard", "window"), "classic");
  assert.equal(shell.layoutOf("?layout=nonsense", "switchboard", "window"), "switchboard");
  // a layout named in the address is for this load, and read alongside
  // the page's own parameters
  assert.equal(shell.layoutOf("?view=usage&layout=switchboard", "", "window"), "switchboard");
  // the tray panel keeps its tabs until its own switchboard
  assert.equal(shell.layoutOf("?mode=panel", "switchboard", "panel"), "classic");
  assert.equal(shell.layoutOf("", "switchboard", "panel"), "classic");
});

test("redirectOf: the switchboard stage is the landing page", () => {
  assert.deepEqual(shell.redirectOf("", ""), { page: "switchboard", inspect: null });
  assert.deepEqual(shell.redirectOf("?view=switchboard", ""), { page: "switchboard", inspect: null });
  assert.deepEqual(shell.redirectOf("?view=agents", ""), { page: "switchboard", inspect: null });
  assert.deepEqual(shell.redirectOf("?view=providers", ""), { page: "switchboard", inspect: null });
  assert.deepEqual(shell.redirectOf("?view=gateway", ""), { page: "switchboard", inspect: null });
  // a provider open in its editor keeps which one
  assert.deepEqual(shell.redirectOf("?view=providers&edit=deepseek", ""),
    { page: "switchboard", inspect: null, edit: "deepseek" });
});

test("redirectOf: sessions, the library and requests move into the inspector", () => {
  assert.deepEqual(shell.redirectOf("?view=sessions", ""), { page: "switchboard", inspect: "sessions" });
  assert.deepEqual(shell.redirectOf("?view=sessions&id=abc123", ""),
    { page: "switchboard", inspect: "sessions", id: "abc123" });
  assert.deepEqual(shell.redirectOf("?view=library", ""), { page: "switchboard", inspect: "library" });
  assert.deepEqual(shell.redirectOf("?view=library&tab=mcp", ""),
    { page: "switchboard", inspect: "library", tab: "mcp" });
  assert.deepEqual(shell.redirectOf("?view=routing", ""), { page: "switchboard", inspect: "requests" });
  assert.deepEqual(shell.redirectOf("?view=routing&req=42", ""),
    { page: "switchboard", inspect: "requests", req: "42" });
  // a hash link names a page the same way
  assert.deepEqual(shell.redirectOf("", "#sessions"), { page: "switchboard", inspect: "sessions" });
  // the query wins over the hash
  assert.deepEqual(shell.redirectOf("?view=usage", "#sessions"),
    { page: "insights", inspect: null, tab: null });
});

test("redirectOf: insights is the Usage page; settings and plugins keep theirs", () => {
  assert.deepEqual(shell.redirectOf("?view=usage", ""), { page: "insights", inspect: null, tab: null });
  assert.deepEqual(shell.redirectOf("?view=usage&tab=requests", ""),
    { page: "insights", inspect: null, tab: "requests" });
  assert.deepEqual(shell.redirectOf("?view=settings", ""), { page: "settings", inspect: null, tab: null });
  assert.deepEqual(shell.redirectOf("?view=settings&tab=network", ""),
    { page: "settings", inspect: null, tab: "network" });
  assert.deepEqual(shell.redirectOf("?view=plugins", ""), { page: "plugins", inspect: null, tab: null });
});
