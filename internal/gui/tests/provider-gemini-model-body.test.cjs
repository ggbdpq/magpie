const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const label = "Send model in Gemini request body";
const hint = "For Gemini relays that require a model field beside the URL. Uses the routed upstream model, including when you switch models. Leave off for Google's API.";

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: Gemini body model is opt-in, saved, reopened and disabled`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(10000);
        const posts = [], errors = [];
        const provider = {
          id: "relay", name: "Gem Relay", icon: "generic", gemini: "https://relay.example/v1beta", baseAPI: "gemini",
          models: [{ id: "gemini-one", name: "Gemini One", on: true }], agents: [], headers: {}, fallback: [],
          key: { set: true, masked: "key" }, keyList: [], balanceToken: { takes: false, set: false },
        };
        const providers = { providers: [provider], presets: [], excluded: [], gateway: { running: true, window: true } };
        page.on("pageerror", (error) => errors.push(error.message));
        await page.route("**/*", async (route) => {
          const url = new URL(route.request().url());
          if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs={lang:${JSON.stringify(lang)},theme:"light",web:true};` });
          if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window={};" });
          if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
          if (url.pathname === "/api/providers") return route.fulfill({ json: providers });
          if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
          if (url.pathname === "/api/provider/save") {
            const body = route.request().postDataJSON();
            posts.push(body);
            provider.geminiModelInBody = body.geminiModelInBody;
            return route.fulfill({ json: providers });
          }
          if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
          const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
          try {
            return route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] });
          } catch {
            return route.fulfill({ status: 404, body: "" });
          }
        });
        await page.goto("http://magpie.test/?view=providers");
        const words = await page.evaluate(([lang, keys]) => Object.fromEntries(keys.map((key) => [key, (I18N[lang] || {})[key] || key])), [lang, [label, hint, "Save"]]);
        const open = async () => {
          await page.locator(".row.provider", { hasText: "Gem Relay" }).click();
          await page.locator(".editor .bar").waitFor();
        };
        await open();
        const advanced = page.locator(".editor details.advanced");
        if (!(await advanced.evaluate((element) => element.open))) await advanced.locator(":scope > summary").click();
        const row = page.locator(".editor .gemini-model-body");
        const checkbox = row.locator("input[type=checkbox]");
        await row.waitFor();
        assert.equal(await checkbox.isChecked(), false);
        assert.ok((await row.textContent()).includes(words[label]));
        assert.ok((await advanced.textContent()).includes(words[hint]));
        if (lang !== "en") assert.notEqual(words[label], label);
        await checkbox.check();
        await page.locator(".editor .bar").getByRole("button", { name: words.Save, exact: true }).click();
        await page.waitForFunction(() => !document.querySelector(".editor"));
        assert.equal(posts.at(-1).geminiModelInBody, true);
        await open();
        assert.equal(await advanced.evaluate((element) => element.open), true);
        await checkbox.waitFor();
        assert.equal(await checkbox.isChecked(), true);
        await checkbox.uncheck();
        await page.locator(".editor .bar").getByRole("button", { name: words.Save, exact: true }).click();
        await page.waitForFunction(() => !document.querySelector(".editor"));
        assert.equal(posts.at(-1).geminiModelInBody, false);
        assert.deepEqual(errors, []);
      });
    }
  }
}
