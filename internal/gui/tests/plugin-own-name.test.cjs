// Run with Node's test runner and Playwright on the module path; see README.md.
// MYCF711 (#4558): a plugin installed from a git repository that says what
// it is in its own package.json (magpie.name) was listed by its dependency
// key in Settings → Plugins → Installed, while community packages showed
// the name the market gives them. A row now shows the plugin's own
// magpie.name where the market names no entry of the package; a package
// the market does name keeps the market's name. The API is faked.
//
// The row's title is the plugin's own data, not the page's words, so one
// language carries the case; the name here is Chinese as the issue's is.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const OWN = "华为云 HUAWEI";
// installed: what /api/plugins says is; listings: the market, which names
// the community package alone
const MINE = [
  { spec: "github:officeace/magpie-officeace", package: "magpie-officeace", version: "0.2.4", name: OWN, providers: [], moved: [] },
  { spec: "github:magicrew/magpie-magicrew-auth", package: "magpie-magicrew-auth", version: "1.0.0", providers: [], moved: [] },
  { spec: "@magpie-community/workbuddy", version: "1.0.0", name: "the package's own", providers: [], moved: [] },
];
const LISTINGS = [{ package: "@magpie-community/workbuddy", name: "WorkBuddy", community: true }];

function server() {
  const state = () => ({ bun: true, bunVersion: "1.4.2", plugins: MINE, picker: false, movable: [] });
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"en",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang: "en", theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/plugins") return json(state());
    if (url.pathname === "/api/plugins/listings") return json({ listings: LISTINGS });
    if (url.pathname === "/api/plugins/npm") return json({ npm: {} });
    if (url.pathname === "/api/plugins/updates") return json({ waiting: [], updated: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    if (!/^\/[\w./-]*$/.test(url.pathname) || url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// what the page asked the API and hasn't drawn yet: an ask counts until
// its body is read and the task that read it has run, which is where the
// page draws its answer
function counting() {
  let n = 0;
  const f = window.fetch;
  const done = () => setTimeout(() => n--, 0);
  window.fetch = async (...a) => {
    if (!String(a[0]).startsWith("/api/")) return f(...a);
    n++;
    let r;
    try { r = await f(...a); } catch (e) { done(); throw e; }
    if (r.status === 204) { done(); return r; }
    for (const k of ["text", "json"]) {
      const read = r[k].bind(r);
      r[k] = () => read().finally(done);
    }
    return r;
  };
  window.asking = () => n;
  try { localStorage.setItem("magpie.pluginTab", "installed"); } catch {}
}

// every answer drawn, and the list done arriving
const settled = (page) => page.waitForFunction(() => window.asking() === 0 && !document.querySelector(".pm-arrive"), null, { polling: "raf" });

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium"])) {
  test(engine + ": an installed plugin's own magpie.name is what its row shows", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" });
    t.after(() => browser.close());
    const ctx = await browser.newContext({ viewport: { width: 980, height: 860 } });
    await ctx.addInitScript(counting);
    const page = await ctx.newPage();
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", server());
    await page.goto("http://magpie.test/?view=plugins", { timeout: 30000 });
    await page.locator("#view-plugins").waitFor({ state: "visible" });
    await settled(page);
    const rows = page.locator("#view-plugins .pm-list .pm-row .name");
    await rows.first().waitFor({ state: "visible" });
    assert.equal(await rows.count(), 3, "three rows");
    const shown = {};
    for (let i = 0; i < 3; i++) shown[await rows.nth(i).locator("> span").first().textContent()] = true;
    assert.ok(shown[OWN], `the plugin's own magpie.name is shown, got ${Object.keys(shown).join(" | ")}`);
    assert.ok(shown["magpie-magicrew-auth"], "one giving none is shown by its dependency key as before");
    assert.ok(shown["WorkBuddy"], "a package the market names keeps the market's name");
    assert.deepEqual(errors, [], "no page errors");
  });
}
