// Run with Node's test runner and Playwright on the module path; see README.md.
// #5895: an account removed from its provider stayed on Routing › Accounts
// and keys all day, counted in the fold's "{n} available". The fold seeds
// itself from the day's tallies (#3351) and from every request the gateway
// still keeps, and both still name the removed one. The history read now
// says what each provider serves now (`serves`), and the fold keeps its
// entries to those: a removed account is gone from the list and from the
// counts, on the live page only — a day looked at keeps who it had.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const iso = (ms) => new Date(ms).toISOString();
const P = "workbuddy";
const who = (u) => `${u}@work.example`;
// WorkBuddy routed over three accounts today; C ran out and failed every
// try, then the user removed it from the provider. The day's tallies keep
// its five tries, and the requests the gateway still keeps weighed it too.
const tallies = {
  [JSON.stringify([P, who("a")])]: { id: P + "@" + who("a"), provider: P, name: "WorkBuddy", who: who("a"), kind: "account", agent: P, model: "m", tried: 12, ok: 12 },
  [JSON.stringify([P, who("b")])]: { id: P + "@" + who("b"), provider: P, name: "WorkBuddy", who: who("b"), kind: "account", agent: P, model: "m", tried: 8, ok: 6, fails: { credit: 2 } },
  [JSON.stringify([P, who("c")])]: { id: P + "@" + who("c"), provider: P, name: "WorkBuddy", who: who("c"), kind: "account", agent: P, model: "m", tried: 5, ok: 0, fails: { credit: 4, quota: 1 } },
};
// what WorkBuddy serves now: A and B; C is removed
const serves = [P, JSON.stringify([P, who("a")]), JSON.stringify([P, who("b")])];
// two kept requests, planned over all three as rotate did before the
// removal: each weighed C, tried it, handed the request on to A
const seat = (u) => ({ id: P + "@" + who(u), provider: P, name: "WorkBuddy", who: who(u), kind: "account", agent: P, model: "m" });
const routes = [1, 2].map((i) => ({
  id: 10 + i, seq: 10 + i, time: iso(now - (90 - i) * 1000), agent: "app", model: "m", provider: P, done: true, status: 200, ms: 800,
  order: [seat("a"), seat("b"), seat("c")],
  tries: [
    { id: P + "@" + who("c"), model: "m", start: iso(now - (90 - i) * 1000), done: true, status: 429, fail: "credit", ms: 200 },
    { id: P + "@" + who("a"), model: "m", start: iso(now - (90 - i) * 1000 + 200), done: true, status: 200, ms: 600 },
  ],
}));

function serve() {
  const state = { agents: [{ id: "app", name: "App", path: "/test/app", fields: [] }], profiles: [], settings: { lang: "en", theme: "light", quotaLeft: false } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"en",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 12, totals: { requests: 14, rerouted: 2, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [{ day: new Date(now).toISOString().slice(0, 10), requests: 14 }], routes: [], tallies, serves });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: Accounts and keys drops an account its provider no longer serves`, async () => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    try {
      const ctx = await browser.newContext({ viewport: { width: 1100, height: 760 }, reducedMotion: "reduce" });
      const page = await ctx.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve());
      await page.goto("http://magpie.test/?view=routing");
      const acts = page.locator(".rt-acts");
      // A is there with its day
      const a = acts.locator(".rt-act", { hasText: who("a") }).first();
      await a.waitFor();
      // B's counts are the day's tallies (the kept requests would say 1)
      const b = acts.locator(".rt-act", { hasText: who("b") }).first();
      assert.deepEqual(await b.locator(".tally span").allTextContents(), ["tried 8", "answered 6", "2 out of credit"]);
      // C, removed from the provider, is gone — though its day is in the
      // tallies and the kept requests still weigh it
      assert.equal(await acts.locator(".rt-act", { hasText: who("c") }).count(), 0);
      // two accounts left, one row each: no fold row counting three
      const names = await acts.locator(".rt-act:not(.rt-keys) > .nm b, .rt-act.rt-keys .nm b").allTextContents();
      assert.deepEqual(names.map((s) => s.trim()), [who("a"), who("b")]);
      assert.equal(await acts.locator(".rt-act.rt-keys").count(), 0);
      assert.deepEqual(errors, []);
    } finally {
      await browser.close();
    }
  });
}
