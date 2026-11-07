// Run with Node's test runner and Playwright on the module path; see README.md.
// Usage › Context's Tuned for you was blank for an agent with no setting
// of tune's (Qoder) and said "not enough calls", which was not why
// (#5627): the pane names the agents it has no card for, and why — no
// setting tune can put in, or files magpie may not read — and says "not
// enough calls" only when that is really why. In English and Chinese, on
// Chromium.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.resolve(__dirname, "../assets"));
const facts = { calls: 4100, streams: 60, compacts: 0, peak: 166000, after: 57000, growth: 1000, rework: 4700, quick: 0.95, pause: 0.045, away: 0.005, hour: 0.9 };
const codex = { agent: "codex", calls: 900, advice: [
  { knob: "compact", setting: {}, compact: { current: 200000, best: 160000, cost: 2e8, low: 1.6e8, curve: [{ value: 100000, cost: 3e8 }, { value: 160000, cost: 1.6e8 }, { value: 200000, cost: 2e8 }], facts } },
] };
const cases = {
  // jackyfan01's pane: Qoder CN's calls were plenty, Claude Code's files
  // were never allowed, and not a card to show — the reasons, not "too few"
  "why the agents are blank": { tune: { days: 7, agents: [], others: ["qoder-cn"], unread: ["claude"] }, state: ["qoder-cn"] },
  // a quiet range really is a quiet range
  "too few, truly": { tune: { days: 7, agents: [], others: [], unread: [] }, state: [] },
  // cards and notes at once: Codex advised, Qoder named
  "cards beside the notes": { tune: { days: 7, agents: [codex], others: ["qoder-cn"], unread: [] }, state: ["qoder-cn"] },
};

function serve(lang, tune, qoder) {
  const state = { agents: [
    { id: "claude", name: "Claude Code", path: "/test/claude", fields: [] },
    { id: "codex", name: "Codex", path: "/test/codex", fields: [] },
    ...(qoder ? [{ id: "qoder-cn", name: "Qoder CN", path: "/test/qoder", fields: [] }] : []),
  ], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/tune") return json(tune);
    if (url.pathname === "/api/context") return json({ days: 7, agents: [], sessions: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType, status: 200 });
  };
}

const few = { en: "Not enough calls in this range to tune on", zh: "这段时间的调用太少，还算不出建议" };
const noSetting = { en: "Qoder CN has no setting to tune on", zh: "Qoder CN 没有可调的设置" };
const unread = { en: "Claude Code's session history isn't being read: Settings › Privacy turns it on", zh: "Claude Code 的会话历史未允许读取：可在 设置 › 隐私 › 智能体的会话历史 里开启" };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium"])) {
  for (const [name, { tune, state: qoder }] of Object.entries(cases)) test(`${engine}, ${name}: Tuned for you says why an agent is blank`, async (t) => {
    const browser = await chromium.launch({ channel: "chromium" });
    t.after(() => browser.close());
    for (const lang of Object.keys(few)) {
      await t.test(lang, async () => {
        const ctx = await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.usageTab", "context"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, tune, qoder.includes("qoder-cn")));
        await page.goto("http://magpie.test/?view=usage");
        await page.locator(".ctx-tune-note").waitFor({ timeout: 10000 });
        const body = await page.locator("#contextPane").innerText();
        assert.deepEqual(errors, []);
        if (name === "too few, truly") {
          assert.ok(body.includes(few[lang]), "the quiet range says so: " + body);
          return;
        }
        assert.ok(!body.includes(few[lang]), "no card is not always too few calls: " + body);
        assert.ok(body.includes(noSetting[lang]), "Qoder named, its why told: " + body);
        if (name === "why the agents are blank") assert.ok(body.includes(unread[lang]), "the unread agent's why told: " + body);
        if (name === "cards beside the notes") {
          await page.locator(".ctx-tune-row").waitFor({ timeout: 10000 });
          assert.ok(!body.includes(few[lang]), "the note stays honest beside the cards: " + body);
        }
      });
    }
  });
}
