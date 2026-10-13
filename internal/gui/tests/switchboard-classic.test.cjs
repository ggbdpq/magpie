// Run with Node's test runner and Playwright on the module path; see README.md.
// The switchboard (shell.js) is a preview the reader turns on in Settings ›
// General or with ?layout=switchboard; until it is the default, a load
// without the flag is the classic window, pixel for pixel: the sidebar from
// 1024 points, the header's tabs under it, no stage, no inspector. With it
// on, the stage is the landing page, the header holds the Switchboard /
// Insights switch, the sidebar stays out of the way, and the old
// addresses land where the design puts them (Sessions keeps its own page
// until the inspector's tabs arrive). English and Chinese, Chromium and
// WebKit; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const words = {
  en: { switchboard: "Switchboard", insights: "Insights", classic: "Classic", row: "Switchboard layout", sources: "Model sources" },
  zh: { switchboard: "接线台", insights: "观测", classic: "经典", row: "接线台布局", sources: "模型来源" },
};

function serve(lang, providers = []) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json({ providers, presets: [], excluded: [], gateway: { url: "http://127.0.0.1:3425", running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function open(t, engine, lang, { layout = null, url = "http://magpie.test/", width = 1280, waitFor = null, providers = [] } = {}) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const context = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
  if (layout) await context.addInitScript((v) => localStorage.setItem("magpie.layout", v), layout);
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, providers));
  await page.goto(url);
  await page.waitForSelector(waitFor || "#view-agents:not([hidden]), #view-switchboard:not([hidden])");
  return { page, errors };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, w] of Object.entries(words)) {
    test(`${engine} ${lang}: classic is the default and unchanged`, async (t) => {
      const { page, errors } = await open(t, engine, lang);
      assert.ok(await page.evaluate(() => !document.body.classList.contains("switchboard")), "no switchboard without the flag");
      assert.ok(await page.evaluate(() => document.body.classList.contains("side-nav")), "the sidebar from 1024 points");
      assert.ok(await page.locator("#side .side-scroll #nav").isVisible(), "the pages in the sidebar");
      assert.ok(await page.locator("#view-agents").isVisible(), "the Agents page");
      assert.equal(await page.locator("#view-switchboard").count(), 1, "the stage's mount is there");
      assert.ok(!(await page.locator("#view-switchboard").isVisible()), "but shows nothing");
      assert.ok(!(await page.locator("#sbInspector").isVisible()), "nor does the inspector");
      assert.equal(await page.locator(".sb-seg").count(), 0, "no switchboard switch in the header");

      // the layout pick is in Settings › General, Classic the one on
      await page.click("#prefs");
      await page.locator("#layoutRow:not([hidden])").waitFor();
      assert.equal((await page.locator("#layoutRow .name").textContent()).trim(), w.row);
      const picked = await page.locator("#layoutSegs button.on").textContent();
      assert.equal(picked.trim(), w.classic);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: the switchboard, from the kept pick`, async (t) => {
      const { page, errors } = await open(t, engine, lang, { layout: "switchboard" });
      assert.ok(await page.evaluate(() => document.body.classList.contains("switchboard")), "the flag turns it on");
      assert.ok(await page.evaluate(() => !document.body.classList.contains("side-nav")), "the sidebar stays away");
      assert.ok(!(await page.locator("#nav").isVisible()), "the classic tabs are away");
      await page.locator("#view-switchboard:not([hidden])").waitFor();
      assert.ok(await page.locator(".sb-seg").isVisible(), "the Switchboard / Insights switch");
      const seg = page.locator(".sb-seg button");
      assert.deepEqual((await seg.allTextContents()).map((s) => s.trim()), [w.switchboard, w.insights]);
      assert.equal((await page.locator(".sb-seg button.on").textContent()).trim(), w.switchboard);
      // the two banks named, the inspector's container present but away
      assert.ok(await page.locator("#view-switchboard .sb-right .sb-h").textContent().then((s) => s.includes(w.sources)), "the right bank is model sources");
      assert.equal(await page.locator("#sbInspector").count(), 1);
      assert.ok(!(await page.locator("#sbInspector").isVisible()));
      // a reload's address
      await page.waitForFunction(() => location.search.includes("view=switchboard"));

      // Insights is the Usage page; the switch follows where the reader is
      await seg.nth(1).click();
      await page.locator("#view-usage:not([hidden])").waitFor();
      assert.ok(!(await page.locator("#view-switchboard").isVisible()), "the stage gives way");
      assert.equal((await page.locator(".sb-seg button.on").textContent()).trim(), w.insights);
      await seg.nth(0).click();
      await page.locator("#view-switchboard:not([hidden])").waitFor();
      assert.equal((await page.locator(".sb-seg button.on").textContent()).trim(), w.switchboard);

      // Settings is still the gear's
      await page.click("#prefs");
      await page.locator("#view-settings:not([hidden])").waitFor();
      assert.equal(await page.locator(".sb-seg button.on").count(), 0, "neither switch entry on Settings");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: ?layout=classic wins over the kept pick`, async (t) => {
      const { page, errors } = await open(t, engine, lang, { layout: "switchboard", url: "http://magpie.test/?layout=classic" });
      assert.ok(await page.evaluate(() => !document.body.classList.contains("switchboard")), "the query wins");
      assert.ok(await page.locator("#view-agents").isVisible());
      assert.ok(await page.evaluate(() => document.body.classList.contains("side-nav")), "the classic sidebar");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: an old Sessions link still lands on Sessions`, async (t) => {
      const { page, errors } = await open(t, engine, lang, { layout: "switchboard", url: "http://magpie.test/?view=sessions", waitFor: "#view-sessions:not([hidden])" });
      assert.ok(await page.evaluate(() => document.body.classList.contains("switchboard")), "the shell stays");
      assert.ok(!(await page.locator("#view-switchboard").isVisible()), "the stage is not in front of it");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: an old Providers edit link opens its editor on a cold load`, async (t) => {
      const provider = { id: "relay", name: "Relay", icon: "generic", host: "relay.test", chat: "https://relay.test/v1",
        responses: "", anthropic: "", models: [{ id: "fixture-model", name: "Fixture model", on: true }], agents: [], fallback: [], headers: {},
        key: { set: true, masked: "sk-…1234" }, keyList: [], ready: true };
      const { page, errors } = await open(t, engine, lang, {
        layout: "switchboard", url: "http://magpie.test/?view=providers&edit=relay",
        providers: [provider], waitFor: "#modal:not([hidden])",
      });
      assert.ok(await page.evaluate(() => document.body.classList.contains("switchboard")), "the shell stays on");
      assert.ok(await page.locator("#view-providers").isVisible(), "the editor's page stays available until the inspector arrives");
      assert.equal(await page.locator('#modal input[type="url"]').first().inputValue(), provider.chat);
      assert.equal(await page.evaluate(() => new URLSearchParams(location.search).get("edit")), "relay", "reload keeps the editor");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: the Settings pick switches layouts without a reload`, async (t) => {
      const { page, errors } = await open(t, engine, lang);
      await page.click("#prefs");
      const seg = page.locator("#layoutSegs button");
      await seg.nth(0).waitFor();
      // Switchboard on: a Settings redraw between press and release keeps the pick.
      await seg.nth(0).hover();
      await page.mouse.down();
      assert.ok(await seg.nth(0).evaluate((button) => { renderSettings(); return !button.isConnected; }), "the layout control participates in Settings redraws");
      await page.mouse.up();
      await page.waitForFunction(() => localStorage.getItem("magpie.layout") === "switchboard");
      await page.locator("#view-switchboard:not([hidden])").waitFor();
      assert.ok(await page.evaluate(() => document.body.classList.contains("switchboard")));
      assert.equal(await page.evaluate(() => localStorage.getItem("magpie.layout")), "switchboard");
      assert.equal((await page.locator("#layoutSegs button.on").textContent()).trim(), w.switchboard);
      assert.ok(await page.locator(".sb-seg").isVisible(), "the header switch appears");
      assert.equal(await page.locator("body").evaluate((body) => getComputedStyle(body).paddingLeft), "0px", "no empty sidebar gutter");
      await page.locator(".sb-seg button").nth(1).click();
      await page.locator("#view-usage:not([hidden])").waitFor();
      assert.ok(await page.locator("#usageTab").isVisible(), "Insights' tabs stay reachable without a sidebar");
      // Classic on (from Settings again: the switch took the stage):
      // everything back, the sidebar too
      await page.click("#prefs");
      await page.locator("#view-settings:not([hidden])").waitFor();
      await seg.nth(1).click();
      await page.locator("#view-agents:not([hidden])").waitFor();
      assert.ok(await page.evaluate(() => !document.body.classList.contains("switchboard")));
      assert.ok(await page.evaluate(() => document.body.classList.contains("side-nav")), "the sidebar returns");
      assert.equal(await page.evaluate(() => localStorage.getItem("magpie.layout")), "classic");
      assert.equal(await page.locator(".sb-seg").count(), 0, "the header switch is gone");
      assert.deepEqual(errors, []);
    });
  }
}
