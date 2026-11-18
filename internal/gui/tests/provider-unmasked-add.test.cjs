// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider added at the user's own computer or on their local network
// starts with "Send requests unmasked" already on (the owner, 11-18): their
// machine holds whatever they say to it, so masking keeps nothing from them
// there. The switch is on, under Advanced, for a local preset as its editor
// opens and as a custom provider's Base URL turns local, and it is what Add
// sends; a vendor's stays off, the user's own untick stands over the
// default, and a saved provider keeps what it was saved with. English,
// Chinese (Simplified and Traditional), Japanese and German; no backend, the
// API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const presets = [
  { id: "openai", name: "OpenAI", icon: "openai", kind: "vendor", chat: "https://api.openai.com/v1", added: false },
  { id: "ollama", name: "Ollama", icon: "ollama", kind: "local", chat: "http://localhost:11434/v1", anthropic: "http://localhost:11434", noKey: true, note: "your local models", added: false },
];
// one provider already added, so the list is not the empty welcome
const base = {
  icon: "generic", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "qwen3", name: "Qwen3", on: true }], agents: [], fallback: [], headers: {},
  key: { set: false, masked: "", optional: true }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", searches: false, unredacted: false,
};
const home = { ...base, id: "relay", name: "Relay", chat: "https://api.relay.example/v1" };

function server(lang, saves, asked, linked) {
  const list = { providers: [home], presets, excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(list);
    if (url.pathname === "/api/provider/save") { saves.push(route.request().postDataJSON()); return json(list); }
    // a magpie://import link, fetched once and shown for the user to add
    if (url.pathname === "/api/import/link1") return json({ provider: linked });
    // a name is asked about once, a moment after it is typed
    if (url.pathname === "/api/local-hosts") { asked.push(route.request().postDataJSON()); return json({ local: {} }); }
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a provider added on this computer or the local network starts unmasked", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [900, 440]) {
        // a page on the Providers view with the Add sheet closed
        const open = async (t, { query = "", linked } = {}) => {
          const saves = [], asked = [];
          const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          t.after(() => page.context().close());
          await page.route("**/*", server(lang, saves, asked, linked));
          await page.goto("http://magpie.test/?view=providers" + query);
          const ed = page.locator(".editor.new");
          const box = ed.locator("label.tick.unmasked input");
          // the editor's own bar: a new one's last button is Add
          const pressAdd = async () => {
            const n = saves.length;
            await ed.locator(".bar button").last().click();
            for (let i = 0; i < 50 && saves.length === n; i++) await page.waitForTimeout(50);
            assert.equal(saves.length, n + 1, "Add sent a Save");
            return saves.at(-1);
          };
          return { page, ed, box, saves, asked, errors, pressAdd };
        };
        const unfold = async (ed) => {
          const adv = ed.locator("details.advanced");
          await adv.waitFor({ state: "attached" });
          if (!(await adv.evaluate((d) => d.open))) await adv.locator(":scope > summary").click();
          return adv;
        };

        await t.test(`${lang} ${width}: a local preset opens with the switch on, and is added so`, async (st) => {
          const { page, ed, box, asked, errors, pressAdd } = await open(st);
          await page.locator("#addProvider").click();
          await page.locator("#addSheet .tile .n", { hasText: /^Ollama$/ }).click();
          await ed.locator(".ehead b", { hasText: "Ollama" }).waitFor();
          // under Advanced, which stayed folded: the default hides nothing
          assert.equal(await box.evaluate((c) => !!c.closest("details.advanced")), true, "the switch is under Advanced");
          assert.equal(await ed.locator("details.advanced").evaluate((d) => d.open), false, "Advanced stays folded");
          assert.equal(await box.isChecked(), true, "opens with the switch on");
          const saved = await pressAdd();
          assert.equal(saved.preset, "ollama");
          assert.equal(saved.unredacted, true);
          assert.equal("chat" in saved, false, "the preset's own URLs, nothing moved");
          assert.deepEqual(asked, [], "localhost is known local: nothing is asked about it");
          assert.deepEqual(errors, []);
        });

        await t.test(`${lang} ${width}: a custom provider at a local URL is added unmasked`, async (st) => {
          const { page, ed, box, asked, errors, pressAdd } = await open(st);
          await page.locator("#addProvider").click();
          await page.locator("#addSheet .row-head .custom").click();
          await ed.locator("input").first().fill("Strata");
          await ed.locator("input.base-url").fill("http://127.0.0.1:8080/v1");
          assert.equal(await box.isChecked(), true, "the switch turns on with the local URL");
          const saved = await pressAdd();
          assert.equal(saved.name, "Strata");
          assert.equal(saved.chat, "http://127.0.0.1:8080/v1");
          assert.equal(saved.unredacted, true);
          assert.deepEqual(asked, [], "an IP is known local: nothing is asked about it");
          assert.deepEqual(errors, []);
        });

        await t.test(`${lang} ${width}: a custom provider at a vendor's is added masked`, async (st) => {
          const { page, ed, box, errors, pressAdd } = await open(st);
          await page.locator("#addProvider").click();
          await page.locator("#addSheet .row-head .custom").click();
          await ed.locator("input").first().fill("Relay");
          await ed.locator("input.base-url").fill("https://api.vendor.example/v1");
          assert.equal(await box.isChecked(), false, "a vendor's stays masked");
          const saved = await pressAdd();
          assert.equal(saved.chat, "https://api.vendor.example/v1");
          assert.equal(saved.unredacted, false);
          assert.deepEqual(errors, []);
        });

        await t.test(`${lang} ${width}: a local one the user unticks is added masked`, async (st) => {
          const { page, ed, box, errors, pressAdd } = await open(st);
          await page.locator("#addProvider").click();
          await page.locator("#addSheet .tile .n", { hasText: /^Ollama$/ }).click();
          await ed.locator(".ehead b", { hasText: "Ollama" }).waitFor();
          await unfold(ed);
          assert.equal(await box.isChecked(), true);
          await box.uncheck();
          assert.equal(await box.isChecked(), false);
          const saved = await pressAdd();
          assert.equal(saved.preset, "ollama");
          assert.equal(saved.unredacted, false, "the user's word about it, whatever the address");
          assert.deepEqual(errors, []);
        });

        // a magpie://import link is an add too: one naming a local server is
        // added unmasked, and one naming a vendor's stays masked
        await t.test(`${lang} ${width}: an import link goes in as its addresses say`, async (st) => {
          const local = await open(st, { query: "&import=link1", linked: { id: "box", name: "Box", chat: "http://127.0.0.1:8080/v1" } });
          await local.page.locator(".editor.import").waitFor();
          const added = await local.pressAdd();
          assert.equal(added.chat, "http://127.0.0.1:8080/v1");
          assert.equal(added.unredacted, true, "a link to a local server is added unmasked");
          assert.deepEqual(local.errors, []);

          const away = await open(st, { query: "&import=link1", linked: { id: "relay", name: "Relay", chat: "https://api.vendor.example/v1" } });
          await away.page.locator(".editor.import").waitFor();
          const other = await away.pressAdd();
          assert.equal(other.unredacted, false, "a link to a vendor's is added masked");
          assert.deepEqual(away.errors, []);
        });
      }
    }
  });
}
