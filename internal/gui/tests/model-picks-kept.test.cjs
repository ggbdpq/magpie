// Run with Node's test runner and Playwright on the module path; see README.md.
// #6811 (marsvet): a Google Gemini provider's picks were made on device 1 and
// synced. On device 2 the login's credentials don't come along: after
// signing in there, the account's list is computed locally, and none of the
// saved picks' ids were in it. The editor opened with every listed model
// drawn as served — all of it looking picked — and a Save wrote none of the
// saved picks back, so they were gone. The saved picks open as the editor's
// picks: the ones the account's list doesn't carry now are drawn as picked
// beside the list ("Added by hand"), nothing reads as served under them, and
// Save writes the saved picks as they were. With no picks saved, all as
// before (#681). English and Chinese; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const fetched = new Date(Date.now() - 3600e3).toISOString();
// device 2's account list, computed there: none of the saved picks' ids
// are in it (the plan's list moved on, the id forms with it)
const listed = ["gemini-4-pro", "gemini-4-flash", "gemini-4-flash-lite", "gemini-4-ultra", "gemini-4-nano", "gemini-4-flash-image"].map((id) => ({ id, name: id }));

function providerFor(id, name, chosen, rows) {
  return {
    id, name, icon: "google", host: "", chat: "", responses: "", anthropic: "", catalog: "",
    models: (rows ?? listed).map((m) => ({ ...m, on: rows ? m.on : true })), chosen, fetched, agents: [], fallback: [], headers: {},
    key: { set: false, masked: "", optional: true }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", ready: true,
    account: { agent: id, agentName: name, agentIcon: "google", builtin: "", user: "a@gmail", logins: [{ user: "a@gmail", active: true, on: true }] },
  };
}

function serve(lang, saves) {
  const picks = { gemini: ["gemini-3-pro", "gemini-3-flash"] };
  const providers = () => ({ providers: [providerFor("gemini", "Google Gemini", picks.gemini)], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json(providers());
    if (url.pathname === "/api/provider/save") {
      const body = req.postDataJSON();
      saves.push([body.id, body.models]);
      picks[body.id] = body.models;
      return json(providers());
    }
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": saved picks the account's list lost stay picked (#6811)", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(`${lang}: a save keeps the saved picks`, async () => {
        const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], saves = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, saves));
        await page.goto("http://magpie.test/?view=providers");
        const editor = page.locator("#modal:not([hidden]) .editor");
        await page.locator(".row.provider", { hasText: "Google Gemini" }).first().click();
        await editor.locator(".mchips .mchip").first().waitFor();

        // the saved picks open picked: theirs, not the list's
        const picked = () => editor.locator(".mchips .mchip.on").evaluateAll((cs) => cs.map((c) => c.firstChild.textContent));
        assert.deepEqual(await picked(), ["gemini-3-pro", "gemini-3-flash"]);
        assert.equal(await editor.locator(".mchips .mchip.auto").count(), 0, "none reads as served under saved picks");

        // a save writes the saved picks as they were
        await editor.getByRole("button", { name: lang === "zh" ? "保存" : "Save", exact: true }).click();
        await editor.waitFor({ state: "detached" });
        assert.deepEqual(saves, [["gemini", ["gemini-3-pro", "gemini-3-flash"]]]);
        assert.deepEqual(errors, []);
        await page.context().close();
      });

      await t.test(`${lang}: a pick the list still has stays beside the dormant ones`, async () => {
        const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], saves = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const rows = [...listed.map((m) => ({ ...m, on: false })), { id: "gemini-3-pro", name: "gemini-3-pro", on: true }];
        await page.route("**/*", (route) => {
          const url = new URL(route.request().url());
          if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [providerFor("gemini", "Google Gemini", ["gemini-3-pro", "gemini-3-flash"], rows)], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] } });
          return serve(lang, saves)(route);
        });
        await page.goto("http://magpie.test/?view=providers");
        const editor = page.locator("#modal:not([hidden]) .editor");
        await page.locator(".row.provider", { hasText: "Google Gemini" }).first().click();
        await editor.locator(".mchips .mchip").first().waitFor();
        const picked = () => editor.locator(".mchips .mchip.on").evaluateAll((cs) => cs.map((c) => c.firstChild.textContent));
        assert.deepEqual(await picked(), ["gemini-3-pro", "gemini-3-flash"]);
        await editor.getByRole("button", { name: lang === "zh" ? "保存" : "Save", exact: true }).click();
        await editor.waitFor({ state: "detached" });
        assert.deepEqual(saves, [["gemini", ["gemini-3-pro", "gemini-3-flash"]]]);
        assert.deepEqual(errors, []);
        await page.context().close();
      });

      await t.test(`${lang}: with nothing saved, the list reads as served as before (#681)`, async () => {
        const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], saves = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", (route) => {
          const url = new URL(route.request().url());
          if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [providerFor("gemini", "Google Gemini", [])], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] } });
          return serve(lang, saves)(route);
        });
        await page.goto("http://magpie.test/?view=providers");
        const editor = page.locator("#modal:not([hidden]) .editor");
        await page.locator(".row.provider", { hasText: "Google Gemini" }).first().click();
        await editor.locator(".mchips .mchip").first().waitFor();
        assert.equal(await editor.locator(".mchips .mchip.on").count(), 0);
        assert.equal(await editor.locator(".mchips .mchip.auto").count(), listed.length);
        await editor.getByRole("button", { name: lang === "zh" ? "保存" : "Save", exact: true }).click();
        await editor.waitFor({ state: "detached" });
        assert.deepEqual(saves, [["gemini", []]]);
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
  });
}
