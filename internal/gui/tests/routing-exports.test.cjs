// Run with Node's test runner and Playwright on the module path; see README.md.
// window.magpieRouting is routing.js's read-only doorway for the
// switchboard's bridge (bridge.js): the fold thresholds, where a seat
// shows on the stage (shownRow), the wires to it (wiresTo) and the Usage
// page's reading of an account (cardOf), plus app.js's familyWindows. This
// checks the doorway is frozen and answers with the very objects the
// Routing page is drawn from: a folded key's shownRow is the fold row on
// the stage, not a copy, and cardOf returns the quota card the Usage page
// would show. English, Chromium and WebKit; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const ago = (s) => new Date(now.getTime() - s * 1e3).toISOString();
const M = "agnes-3.0-flash";
// three keys of one provider for one model fold (FOLD_AT); one account
// stands alone, its window read on the Usage page
const order = () => [
  ...[1, 2, 3].map((i) => ({ id: `agnes#k${i}`, provider: "agnes", name: "Agnes", who: `agnes-key-${i}`, kind: "key", model: M })),
  { id: "agnes#a1", provider: "agnes", name: "Agnes", who: "agnes-one", kind: "account", model: M, agent: "agnes" },
];
const group = { id: "flash", name: "Flash", routing: "", members: ["agnes/" + M] };
const first = { id: 1, seq: 1, time: ago(30), agent: "codex", model: "flash", provider: "agnes", done: true,
  status: 200, ms: 900, tokens: 1200, group, order: order(),
  tries: [{ id: "agnes#k1", model: M, start: ago(29), done: true, status: 200, ms: 900 }] };
const quotas = [{ kind: "subscription", provider: "agnes", user: "agnes-one", windows: [{ name: "5 hours", used: 40, limit: 100 }] }];

function serve() {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"en",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang: "en", theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // no live updates in this test
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 1, rerouted: 0, errors: 0 }, routes: [first] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/usage/quotas/history") return json({});
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: magpieRouting answers with the page's own rows`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await browser.newPage({ viewport: { width: 1100, height: 900 }, reducedMotion: "reduce" });
    page.setDefaultTimeout(5000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", serve());
    await page.goto("http://magpie.test/?view=routing");
    await page.locator(".rt-stage .rt-accts li.rt-fold").first().waitFor();
    await page.waitForFunction(() => document.querySelector(".rt-stage .rt-accts li.rt-fold em")?.textContent.trim());

    const out = await page.evaluate(() => {
      const R = window.magpieRouting;
      if (!R) return { missing: true };
      const r = {
        frozen: Object.isFrozen(R),
        FOLD_AT: R.FOLD_AT,
        LONG: R.LONG,
        kinds: ["shownRow", "wiresTo", "cardOf", "familyWindows"].map((k) => typeof R[k]),
      };
      const fold = R.shownRow("agnes#k1");
      r.foldIsLi = fold?.li instanceof HTMLLIElement;
      r.foldShown = !!(fold?.li?.isConnected && fold.li.classList.contains("rt-fold"));
      r.foldIsThePages = fold?.li === document.querySelector(".rt-stage .rt-accts li.rt-fold");
      r.foldCount = fold?.li?.querySelector(".n")?.textContent.trim();
      r.sameFold = R.shownRow("agnes#k2")?.li === fold?.li;
      const acct = R.shownRow("agnes#a1");
      r.acctIsOwnRow = !!(acct?.li?.isConnected && !acct.li.classList.contains("rt-fold"));
      const ws = R.wiresTo(fold);
      r.wiresSvg = ws.length > 0 && ws.every((w) => w instanceof SVGElement);
      r.wiresEndAtRow = ws[ws.length - 1] === fold.wire;
      const card = R.cardOf(acct.w);
      r.card = card ? { provider: card.provider, user: card.user } : null;
      r.cardOfKey = R.cardOf(fold.w);
      return r;
    });

    assert.ok(!out.missing, "window.magpieRouting exists");
    assert.equal(out.frozen, true, "the doorway is frozen");
    assert.equal(out.FOLD_AT, 3);
    assert.equal(out.LONG, 6);
    assert.deepEqual(out.kinds, ["function", "function", "function", "function"]);
    assert.ok(out.foldIsLi && out.foldShown, "a folded key's place is the fold row on the stage");
    assert.ok(out.foldIsThePages, "the very element the page shows, not a copy");
    assert.equal(out.foldCount, "3 keys");
    assert.ok(out.sameFold, "the fold's other key lands on the same row");
    assert.ok(out.acctIsOwnRow, "a lone account is its own row");
    assert.ok(out.wiresSvg && out.wiresEndAtRow, "the wires to a row end at its own");
    assert.deepEqual(out.card, { provider: "agnes", user: "agnes-one" }, "the Usage page's reading of the account");
    assert.equal(out.cardOfKey, null, "a key has no subscription card");
    assert.deepEqual(errors, []);
  });
}
