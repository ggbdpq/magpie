// A plugin that is only a page — or only panels — but ships a host part
// (main or exports) signs in to nothing, yet its Installed row carried a
// Provider chip: the exclusion list on the pv chip has no e.page, and
// inMagpieOnly can't be true for such a plugin, since it needs a window
// with no main and no exports (#7950, zrtzrt: magpie-models-price v2.1.0
// went from the right chips back to pg + pv by adding its host part).
// The API is faked and no request leaves the page.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const PLUGINS = [
  // a page with a host part and no providers: pg alone, no pv
  { spec: "magpie-models-price", providers: [], moved: [], version: "2.1.0", page: { title: "Prices", reads: ["usage totals"] } },
  // panels with a host part, no providers: pn alone, no pv — the same fall-through
  { spec: "magpie-context", providers: [], moved: [], version: "1.0.0", panels: [{ slot: "usage", title: "Context" }], panelReads: ["usage totals"] },
  // a page that does sign in to a provider: pv stays
  { spec: "page-with-provider", providers: ["some-relay"], moved: [], version: "1.0.0", page: { title: "Tokens", reads: [] } },
  // a page with no host part: inMagpieOnly already keeps pv off
  { spec: "page-plain", providers: [], moved: [], version: "1.0.0", page: { title: "Plain", reads: [] }, inMagpieOnly: true },
];

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/plugins") return json({ bun: true, bunVersion: "1.4.2", plugins: PLUGINS, picker: false, movable: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a page or panels alone carries no Provider chip", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const ctx = await browser.newContext({ viewport: { width: 980, height: 860 } });
        const page = await ctx.newPage();
        page.setDefaultTimeout(6000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        try {
          await page.goto("http://magpie.test/?view=plugins");
          const view = page.locator("#view-plugins");
          const installed = { en: "Installed", zh: "已安装" }[lang];
          await page.locator(":is(.lib-tabs .opt, .nav-s):visible", { hasText: installed }).first().click();
          const rows = view.locator(".pm-list .pm-row");
          await rows.first().waitFor();
          const row = (spec) => rows.filter({ hasText: spec }).first();
          const chips = async (spec) => ({
            pv: await row(spec).locator(".pm-chip.kind.pv").count(),
            pg: await row(spec).locator(".pm-chip.kind.pg").count(),
            pn: await row(spec).locator(".pm-chip.kind.pn").count(),
          });

          // the reporter's case: a page with a host part, no providers
          assert.deepEqual(await chips("magpie-models-price"), { pv: 0, pg: 1, pn: 0 });
          // the sibling: panels with a host part, no providers
          assert.deepEqual(await chips("magpie-context"), { pv: 0, pg: 0, pn: 1 });
          // a page that does sign in: the Provider chip stays
          assert.deepEqual(await chips("page-with-provider"), { pv: 1, pg: 1, pn: 0 });
          // a page with no host part: as before
          assert.deepEqual(await chips("page-plain"), { pv: 0, pg: 1, pn: 0 });
          assert.deepEqual(errors, []);
        } finally {
          await ctx.close();
        }
      });
    }
  });
}
