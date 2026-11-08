// Run with Node's test runner and Playwright on the module path; see README.md.
// The Antigravity desktop app's steps tell a speed on the Usage page's
// Requests tab: each step's database row has when it began, when its first
// token came and when it ended (sessions.agAppCall.times), so a step is
// measured over the time after its first token, as a Codex turn of one call
// is. Gemini can send a step's tool call whole (a real step: 599 tokens 0.3 s
// after a first token at 16.6 s), so the same bounds hold: a second or more
// and 100 tokens or more after the first token (usage.Row.antigravityStep);
// a step that tells neither tells the least it wrote at over the whole step
// when that is 1,000 tokens or more (usage.FloorAgents). Every language, both
// engines, wide and narrow; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = process.env.MAGPIE_AG_SPEED_ASSETS || path.resolve(__dirname, "../assets");
const now = Date.now();
const at = (i) => new Date(now - (i + 1) * 60e3).toISOString();
const AG = {
  source: "log", agent: "antigravity", agentName: "Antigravity", icon: "antigravity-color", provider: "session-unknown", providerName: "Local session",
  model: "gemini-4-argon", req: "gemini-4-argon", served: "gemini-4-argon", in: 9000, cache_read: 0, cache_write: 0, status: 0, cost: 0, priced: false,
};
const ROWS = [
  // a real step: 3,681 tokens in the 49.6 s after a 19.1 s first token, 74 tok/s
  { ...AG, t: at(0), out: 3681, ms: 68760, ttft_ms: 19140 },
  // yygutn's step 5 on #2190: 194 tokens in the 5.9 s after its first, 33 tok/s
  { ...AG, t: at(1), out: 194, ms: 9702, ttft_ms: 3800 },
  // 400 tokens in the 999 ms after a 0.3 s first token: could be one push, none
  { ...AG, t: at(2), out: 400, ms: 1299, ttft_ms: 300 },
  // 60 tokens over 2 s: too few to tell, none
  { ...AG, t: at(3), out: 60, ms: 3000, ttft_ms: 1000 },
  // 1,200 tokens in one push 0.5 s after a 9.5 s first token: at least 120
  // over the whole step
  { ...AG, t: at(4), out: 1200, ms: 10000, ttft_ms: 9500 },
];

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/usage/requests") {
      return json({ period: url.searchParams.get("period"), rows: ROWS, offset: 0, total: ROWS.length, calls: ROWS.length, errors: 0, input: 45000, output: 5535, cache_read: 0, cache_write: 0, cost: 0, unpriced: ROWS.length, agents: [{ id: "antigravity", name: "Antigravity", icon: "antigravity-color" }] });
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 0, errors: 0, input: 0, output: 0, reasoning: 0, unpriced: 0, cost: 0, bucket: "day", series: [], agents: [], models: [], path: "" });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], models: [], gateway: { running: false } });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const L = {
  en: (n) => `${n} tok/s`,
  zh: (n) => `${n} token/秒`,
  "zh-TW": (n) => `${n} token/秒`,
  ja: (n) => `${n} トークン/秒`,
  de: (n) => `${n} Token/s`,
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": an Antigravity step tells its speed, a push or a short one none", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) for (const width of [1400, 1100]) {
      const s = L[lang];
      await t.test(lang + " " + width, async () => {
        const errors = [];
        const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        t.after(() => ctx.close());
        const p = await ctx.newPage();
        p.setDefaultTimeout(5000);
        p.setDefaultNavigationTimeout(60000);
        p.on("pageerror", (e) => errors.push(e.message));
        await p.route("**/*", server(lang));
        await p.goto("http://magpie.test/");
        await p.locator('[data-view="usage"]').first().click();
        await p.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
        await p.locator("#ledWrap .led tbody tr").first().waitFor();
        await p.waitForTimeout(200);

        const cell = (i) => p.locator(".led tbody tr.led-row").nth(i).locator("td.speed");
        const text = async (i) => (await cell(i).textContent()).trim();
        assert.equal(await text(0), s(74), "a real step's speed after its first token");
        assert.equal(await text(1), s(33), "yygutn's step");
        assert.equal(await text(2), "—", "400 tokens in 999 ms could be one push");
        assert.equal(await text(3), "—", "60 tokens are too few to tell");
        assert.equal(await text(4), "≥" + s(120), "a push of 1,200 tokens tells the least over the whole step");
        for (const i of [0, 1, 2, 3, 4]) assert((await cell(i).getAttribute("title")).length > 0, "row " + i + " says why in its tooltip");
        assert(await p.evaluate(() => { const v = document.querySelector("#view-usage"); return v.scrollWidth <= v.clientWidth + 1; }), "the page doesn't scroll sideways");
        assert.deepEqual(errors, []);
      });
    }
  });
}
