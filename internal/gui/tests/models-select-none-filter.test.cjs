// Run with Node's test runner and Playwright on the module path; see README.md.
// With a filter, Select all picked only the models the filter shows, while
// Select none unpicked every model, those the filter hid too. Now both act
// on what the filter shows: Select none unpicks those and leaves the other
// picks; with nothing picked yet it unpicks them from what the vendor's
// list serves (dashed), as Select all picks beside it. With no filter it
// unpicks every model and a Save serves none, as before. Every language,
// Chromium and WebKit, 1100 and 420 wide.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = strings();
const fetched = new Date(Date.now() - 3600e3).toISOString();
const relay = (id, name, models, chosen) => ({
  id, name, icon: "generic", host: id + ".example.com", chat: `https://${id}.example.com/v1`, responses: "", anthropic: "", catalog: "",
  speaks: ["chat"], models, chosen, fetched, agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
});
// 30 models, so the filter is drawn (over 24); the vendor's list serves the
// first 24 (dashed): qwen-0..14 and glm-0..8
const QWEN = Array.from({ length: 15 }, (_, i) => `qwen-${i}`);
const GLM = Array.from({ length: 15 }, (_, i) => `glm-${i}`);
const ALL = [...QWEN, ...GLM];
const models = (chosen) => ALL.map((id, i) => ({ id, name: id, on: chosen.length ? chosen.includes(id) : i < 24 }));
const PICKED = ["qwen-1", "qwen-2", "glm-1", "glm-2"];
const picked = relay("picked-relay", "Picked Relay", models(PICKED), PICKED);
const listed = relay("listed-relay", "Listed Relay", models([]), []);

function serve(lang, posts) {
  const providers = { providers: [picked, listed], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (req.method() === "POST" && url.pathname.startsWith("/api/")) posts.push({ path: url.pathname, body: req.postDataJSON() || {} });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const NONE_TIP = "Unpick every model (those the filter shows)";
const sorted = (a) => [...a].sort();

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    for (const width of [1100, 420]) {
      const tr = (k) => (lang === "en" ? k : I18N[lang][k]);
      test(`${engine} ${lang} ${width}: Select none unpicks only what the filter shows`, async (t) => {
        for (const k of [NONE_TIP, "Select all", "Select none", "Save"]) assert(tr(k), `${lang} lacks ${k}`);
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-select-none-filter.png`) });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const posts = [];
        await page.route("**/*", serve(lang, posts));

        const editor = page.locator("#modal:not([hidden]) .editor");
        const open = async (name) => {
          await page.goto("http://magpie.test/?view=providers");
          await page.locator(".row.provider", { hasText: name }).click();
          await editor.locator(".ehead b", { hasText: name }).waitFor();
          await editor.locator(".mchips .mchip").first().waitFor();
        };
        const bulk = (name) => editor.locator(".mbulk").getByRole("button", { name, exact: true });
        const q = editor.locator(".mbulk input");
        const chips = (cls) => editor.locator(`.mchips .mchip${cls}`).evaluateAll((cs) => cs.map((c) => c.querySelector("span").textContent).sort());
        const save = async () => {
          await editor.locator(".bar").getByRole("button", { name: tr("Save"), exact: true }).click();
          for (let i = 0; i < 100 && !posts.some((p) => p.path === "/api/provider/save"); i++) await page.waitForTimeout(30);
          const saved = posts.find((p) => p.path === "/api/provider/save");
          assert(saved, "nothing saved");
          posts.length = 0;
          return saved.body;
        };

        // picks on both sides of the filter: Select none under "glm" leaves
        // the qwen picks, and Select all under it picks every glm beside them
        await open("Picked Relay");
        assert.equal(await bulk(tr("Select none")).getAttribute("title"), tr(NONE_TIP));
        await q.fill("glm");
        await bulk(tr("Select none")).click();
        assert.deepEqual(await chips(".on"), [], "a glm pick left under the filter");
        await q.fill("");
        assert.deepEqual(await chips(".on"), ["qwen-1", "qwen-2"]);
        await q.fill("glm");
        await bulk(tr("Select all")).click();
        await q.fill("");
        assert.deepEqual(await chips(".on"), sorted(["qwen-1", "qwen-2", ...GLM]));
        await q.fill("qwen");
        await bulk(tr("Select none")).click();
        await q.fill("");
        assert.deepEqual(sorted((await save()).models), sorted(GLM));

        // nothing picked: the filter unpicks from what the vendor's list
        // serves, and the rest of it stays picked
        await open("Listed Relay");
        assert.deepEqual(await chips(".auto"), sorted(ALL.slice(0, 24)));
        await q.fill("glm");
        await bulk(tr("Select none")).click();
        await q.fill("");
        assert.deepEqual(await chips(".on"), sorted(QWEN));
        assert.deepEqual(await chips(".auto"), []);
        assert.deepEqual(sorted((await save()).models), sorted(QWEN));

        // no filter: every model unpicked, and a Save serves none
        await open("Picked Relay");
        await bulk(tr("Select none")).click();
        assert.deepEqual(await chips(".on"), []);
        assert.deepEqual(await chips(".auto"), []);
        const none = await save();
        assert.deepEqual(none.models, []);
        assert.equal(none.noModels, true);
        assert.deepEqual(errors, []);
      });
    }
  }
}
