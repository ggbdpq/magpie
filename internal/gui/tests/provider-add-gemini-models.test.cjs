// Run with Node's test runner and Playwright on the module path; see README.md.
// A custom provider with only a Gemini-compatible Base URL can fetch its model
// list from the add form (#8067: 添加仅配置 Gemini 兼容 Base URL 的自定义供应商时，
// 点击「获取模型」误报「请先填写 Base URL」). The add form's Fetch models button
// must post provider/list with the gemini URL typed, not stop at the
// "Type the base URL first" guard that the save and detect paths already pass.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const GEMINI = "https://relay.example.com/antigravity/v1beta";
const listed = [{ id: "gemini-2.5-pro" }, { id: "gemini-2.5-flash" }];

function serve(lang, posts) {
  const providers = { providers: [{ id: "anthropic", name: "Anthropic", icon: "anthropic", preset: "anthropic", models: [], agents: [], key: { set: true, masked: "sk-…ab12" } }], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data, status = 200) => route.fulfill({ json: data, status });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/provider/list") { posts.push({ path: url.pathname, body: route.request().postDataJSON() }); return json({ models: listed }); }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    try { await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { fetch: "Fetch models", first: "Type the base URL first" },
  zh: { fetch: "获取模型", first: "请先填写 Base URL" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(L)) {
    test(`${engine} ${lang}: Fetch models with a Gemini URL alone in the add form`, async (t) => {
      assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=providers");
      const w = L[lang];
      await page.locator("#addProvider").click();
      await page.locator("#addSheet .row-head .custom").click();
      const ed = page.locator(".editor.new");
      await ed.locator(".add-models").waitFor();

      // a Gemini-compatible provider: only the Gemini Base URL and key are typed
      await ed.locator('.segs.base-apis .opt[data-api="gemini"]').click();
      await ed.locator(".base-url").fill(GEMINI);
      await ed.locator('input[type="password"]').first().fill("AIza-typed");
      assert.equal((await ed.locator(".base-url").inputValue()), GEMINI);

      const statusBefore = await page.locator("#status").textContent();
      await ed.getByRole("button", { name: w.fetch, exact: true }).click();
      for (let i = 0; i < 60 && !posts.some((p) => p.path === "/api/provider/list"); i++) await page.waitForTimeout(50);

      const list = posts.find((p) => p.path === "/api/provider/list");
      assert(list, `Fetch models asked the vendor with a Gemini URL alone: status="${(await page.locator("#status").textContent()).trim()}" (was "${(statusBefore || "").trim()}")`);
      assert.equal(list.body.gemini, GEMINI, "the typed Gemini URL is posted");
      assert(!list.body.chat && !list.body.anthropic && !list.body.responses, "no other URL made up");
      await ed.locator(".add-models-list .mchip").first().waitFor();
      assert.equal(await ed.locator(".add-models-list .mchip").count(), listed.length);
      assert.deepEqual(errors, []);
    });
  }
}
