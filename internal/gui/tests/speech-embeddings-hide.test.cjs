// Run with Node's test runner and Playwright on the module path; see README.md.
// Fish-under-sea (#5418): a provider's editor had nowhere to put its speech
// and embedding models — a fetched list's whisper-1, tts-1 and embedding
// models just sat in nothing. Now each is ticked in its own section, a
// click unticks it (and ticks it back), and Save writes the unticked ones
// as hiddenSpeech and hiddenEmbeddings, which the /v1/models kind lists
// then leave out. The API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const fetched = new Date(Date.now() - 3600e3).toISOString();
const speechIds = ["tts-1", "whisper-1"];
const embedIds = ["text-embedding-3-small", "bge-reranker-v2"];

function providerFor(hidden) {
  return {
    id: "openai", name: "OpenAI", icon: "openai", host: "", chat: "https://api.openai.com/v1", responses: "", anthropic: "", catalog: "",
    models: [{ id: "gpt-6", name: "GPT-6", on: true }], chosen: [], fetched, agents: [], fallback: [], headers: {},
    key: { set: true, masked: "sk-…one", optional: false }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", ready: true,
    speechIds, embedIds, hiddenSpeech: hidden.speech, hiddenEmbeddings: hidden.embeds,
  };
}

function settingsPayload(lang, over) {
  return {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", textSize: 100,
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [],
    imageGenAuto: "", workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    ...over,
  };
}

function serve(lang, saves) {
  const hidden = { speech: ["whisper-1"], embeds: [] };
  const providers = () => ({ providers: [providerFor(hidden)], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true,textSize:100};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", textSize: 100 } });
    if (url.pathname === "/api/settings") return json(settingsPayload(lang, {}));
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json(providers());
    if (url.pathname === "/api/provider/save") {
      const body = req.postDataJSON();
      saves.push(body);
      if (body.hiddenSpeech) hidden.speech = body.hiddenSpeech;
      if (body.hiddenEmbeddings) hidden.embeds = body.hiddenEmbeddings;
      return json(providers());
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [], models: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

const words = {
  en: { save: "Save", speech: "Speech models", embeds: "Embedding models" },
  zh: { save: "保存", speech: "语音模型", embeds: "嵌入模型" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium"])) {
  test(engine + ": a provider's speech and embedding models can be unticked", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const open = async (lang, width, saves) => {
      const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.errors = [];
      page.on("pageerror", (e) => page.errors.push(e.message));
      await page.route("**/*", serve(lang, saves));
      await page.goto("http://magpie.test/?view=providers");
      return page;
    };
    for (const lang of Object.keys(words)) {
      const w = words[lang];
      for (const width of [1000, 360]) {
        await t.test(`${lang} at ${width}px: the editor unticks and ticks back speech and embedding models`, async () => {
          const saves = [];
          const page = await open(lang, width, saves);
          const editor = page.locator("#modal:not([hidden]) .editor");
          const chips = (cls) => editor.locator(`.mchips.${cls} .mchip`);
          const ticked = (cls) => editor.locator(`.mchips.${cls} .mchip.on`).evaluateAll((cs) => cs.map((c) => c.textContent));
          const chip = (cls, id) => editor.locator(`.mchips.${cls} .mchip`, { has: page.locator(`span:text-is("${id}")`) });
          await page.locator(".row.provider", { hasText: "OpenAI" }).first().click();
          await editor.locator(".mchips.speech .mchip").first().waitFor();
          await editor.getByText(w.speech, { exact: true }).first().waitFor();
          await editor.getByText(w.embeds, { exact: true }).first().waitFor();
          assert.equal(await chips("speech").count(), 2);
          assert.equal(await chips("embeddings").count(), 2);
          assert.deepEqual(await ticked("speech"), ["tts-1"], "the saved hidden one opens unticked");
          assert.deepEqual(await ticked("embeddings"), ["text-embedding-3-small", "bge-reranker-v2"]);
          await chip("speech", "tts-1").click();
          assert.deepEqual(await ticked("speech"), []);
          await chip("embeddings", "bge-reranker-v2").click();
          assert.deepEqual(await ticked("embeddings"), ["text-embedding-3-small"]);
          await editor.getByRole("button", { name: w.save, exact: true }).click();
          await editor.waitFor({ state: "detached" });
          assert.deepEqual(saves.at(-1).hiddenSpeech, ["tts-1", "whisper-1"]);
          assert.deepEqual(saves.at(-1).hiddenEmbeddings, ["bge-reranker-v2"]);
          await page.locator(".row.provider", { hasText: "OpenAI" }).first().click();
          await editor.locator(".mchips.speech .mchip").first().waitFor();
          assert.deepEqual(await ticked("speech"), [], "reopened as saved");
          assert.deepEqual(await ticked("embeddings"), ["text-embedding-3-small"]);
          assert.deepEqual(page.errors, []);
          await page.context().close();
        });
      }
    }
  });
}
