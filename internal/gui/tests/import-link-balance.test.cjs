// A magpie://import link with balance= (#3187, after wtj-0527's proposal):
// the dialog shows where the key's balance is asked, labelled Balance URL,
// under the endpoints it shares a host with, and Add saves it; a link
// without one shows no such row. All APIs and keys are fixtures.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const assets = process.env.ASSET_DIR || path.resolve(__dirname, "../assets");
const engines = process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"];
const langs = ["en", "zh", "zh-TW", "ja", "de"];

const linked = {
  id: "share-relay", name: "Share Relay", anthropic: "https://share-relay.example", responses: "https://share-relay.example/v1",
  key: "sk-fixture-balance", balanceURL: "https://share-relay.example/v1/usage",
};

async function fixture(t, engine, lang, width, provider) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await browser.newPage({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
  page.setDefaultTimeout(7000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], saves = [];
  page.on("pageerror", (e) => errors.push(e.stack || e.message));
  await page.route("**/*", async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    const state = { providers: [], presets: [], excluded: [], gateway: { running: true, window: true } };
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs={lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(state);
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/import/balance1") return json({ provider });
    if (url.pathname === "/api/provider/save") { saves.push(req.postDataJSON()); return json(state); }
    if (url.pathname === "/api/gateway/trace") return new Promise(() => {});
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  });
  t.after(async () => {
    if (process.env.ARTIFACT_DIR) {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-import-link-balance.png`) });
    }
    await browser.close();
    assert.deepEqual(errors, []);
  });
  await page.goto("http://magpie.test/?view=providers&import=balance1");
  const ed = page.locator(".editor.import");
  await ed.waitFor();
  return { page, ed, saves };
}

for (const engine of engines) {
  for (const lang of langs) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: a link's balance= shows before Add and is saved`, async (t) => {
        const { page, ed, saves } = await fixture(t, engine, lang, width, linked);
        assert.equal(await ed.locator("code.import-balance").textContent(), "https://share-relay.example/v1/usage");
        const label = await page.evaluate(() => t("Balance URL"));
        assert(await ed.getByText(label, { exact: true }).count() >= 1, "the row is labelled in the page's language");
        const fits = await ed.evaluate((e) => e.getBoundingClientRect().right <= innerWidth && e.scrollWidth <= e.clientWidth + 1);
        assert(fits, "the dialog fits the window");
        await ed.locator(".bar button.primary").click();
        await page.waitForFunction(() => !importing);
        assert.equal(saves.length, 1);
        assert.equal(saves[0].balanceURL, "https://share-relay.example/v1/usage");
        assert.equal(saves[0].key, "sk-fixture-balance");
      });
    }
  }
  test(`${engine}: a link without balance= shows no Balance URL row`, async (t) => {
    const { ed } = await fixture(t, engine, "en", 1100, { ...linked, balanceURL: undefined });
    assert.equal(await ed.locator("code.import-balance").count(), 0);
  });
}
