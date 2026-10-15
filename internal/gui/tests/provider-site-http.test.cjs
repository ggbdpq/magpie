// Run with Node's test runner and Playwright on the module path; see README.md.
// The provider editor's site link (#2377, Peterpig): a custom provider whose
// base URL says http://127.0.0.1:7863 opened as https://127.0.0.1:7863,
// because the link was "https://" + p.host and p.host is the host with its
// scheme stripped. The link keeps the scheme the provider's own base URL
// carries; one whose URL says https opens as https as before. No backend: the
// API is faked, the opened address is read off /api/open.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const custom = (id, name, url, host) => ({
  id, name, icon: "generic", preset: "", host,
  chat: url, responses: "", anthropic: "", gemini: "", decide: "", catalog: "",
  models: [{ id: "m1", name: "", on: true }],
  agents: [], fallback: [], headers: {}, keyList: [], proxy: "",
  key: { set: false },
});

function serve(lang, opens) {
  const settings = { theme: "light", lang };
  const providers = {
    providers: [
      custom("local", "Local relay", "http://127.0.0.1:7863/v1", "127.0.0.1:7863"),
      custom("remote", "Remote relay", "https://api.example.com/v1", "api.example.com"),
    ],
    presets: [], excluded: [], gateway: { running: true, window: true },
  };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/open" && route.request().method() === "POST") {
      opens.push(route.request().postDataJSON().url);
      return json(null);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: the editor's site link keeps the base URL's scheme`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [], opens = [];
      const opened = async (n) => { for (let i = 0; i < 50 && opens.length < n; i++) await page.waitForTimeout(100); };
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, opens));
      await page.goto("http://magpie.test/?view=providers");

      // an http custom provider: the link opens http, not https
      await page.locator(".row.provider", { hasText: "Local relay" }).first().click();
      const site = page.locator(".editor .site");
      await site.waitFor();
      assert.equal(await site.textContent(), "127.0.0.1:7863 ↗");
      await site.click();
      await opened(1);
      assert.deepEqual(opens, ["http://127.0.0.1:7863"]);
      await page.keyboard.press("Escape");

      // an https custom provider: unchanged
      await page.locator(".row.provider", { hasText: "Remote relay" }).first().click();
      const site2 = page.locator(".editor .site");
      await site2.waitFor();
      assert.equal(await site2.textContent(), "api.example.com ↗");
      await site2.click();
      await opened(2);
      assert.deepEqual(opens, ["http://127.0.0.1:7863", "https://api.example.com"]);
      assert.deepEqual(errors, [], "no page errors");
    });
  }
}
