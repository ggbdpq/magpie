// Run with Node's test runner and Playwright on the module path; see README.md.
// The group editor's "Try": what a draft's rules would decide for a
// conversation's first turn saying the words given — the classifier asked
// for real, the matching rule named, nothing routed and nothing kept. In
// every language the try speaks the page's words, and a group that
// decides nothing says so.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "a/fast", name: "fast", providerName: "A", icon: "generic", context: 256000, ready: true },
  { id: "b/opus", name: "opus", providerName: "B", icon: "generic", context: 1000000, ready: true },
];
const groups = () => ({
  models,
  groups: [{ id: "main", name: "Main", members: ["a/fast", "b/opus"], routing: "order", ready: true, classifier: "c/cls",
    memberInfo: [{ id: "a/fast", ready: true, context: 256000 }, { id: "b/opus", ready: true, context: 1000000 }],
    rules: [{ use: "b/opus", intent: "complex" }] }],
  pools: [],
});
const hit = { n: 1, use: "b/opus", when: ['intent "complex"'], turn: 1, tokens: 4200,
  classified: { by: "c/cls", intents: ["complex"], intent: "complex", ms: 350, sure: 0.9 } };
const none = { n: 0, turn: 1, tokens: 4200, classified: { by: "c/cls", intents: ["complex"] } };

const words = {
  en: { label: "Tryout", placeholder: "Try these rules on a message", button: "Try these rules", rule: "would match", noRule: "No rule would match", nothing: "decides nothing", real: "A real request also depends on" },
  zh: { label: "试算", placeholder: "输入一条消息，试试这些规则", button: "试试这些规则", rule: "会命中", noRule: "没有规则会命中", nothing: "不参与路由决策", real: "真实请求还取决于" },
  "zh-TW": { label: "試算", placeholder: "輸入一則訊息，試試這些規則", button: "試試這些規則", rule: "會命中", noRule: "沒有規則會命中", nothing: "不參與路由決策", real: "真實請求還取決於" },
  ja: { label: "試算", placeholder: "メッセージを入力してルールを試す", button: "ルールを試す", rule: "が一致し", noRule: "一致するルールはありません", nothing: "ルーティングを決めません", real: "実際のリクエストはさらに" },
  de: { label: "Ausprobieren", placeholder: "Diese Regeln an einer Nachricht ausprobieren", button: "Regeln ausprobieren", rule: "würde treffen", noRule: "Keine Regel würde treffen", nothing: "entscheidet nichts", real: "Eine echte Anfrage hängt auch davon ab" },
};

function serve(lang, tried) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname === "/api/groups/try") {
      // the body the editor sent carries the draft: it has the rule asked about
      const body = r.request().postDataJSON();
      assert.equal(body.rules.length, 1, "the draft's rules are sent");
      assert.equal(body.text.length > 0, true, "the words to try are sent");
      return json({ hit: tried });
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function open(t, engine, lang, tried) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width: 1100, height: 1400 } })).newPage();
  page.setDefaultTimeout(5000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, tried));
  await page.goto("http://magpie.test/?view=routing");
  await page.locator(".rt-group").first().waitFor();
  await page.locator(".rt-group").first().click();
  await page.locator(".rt-gedit").waitFor();
  return { page, errors };
}

async function tryRules(page, lang, ...expect) {
  const w = words[lang];
  const box = page.getByPlaceholder(w.placeholder);
  await box.waitFor();
  await box.fill("a hard task");
  await page.getByRole("button", { name: w.button, exact: true }).click();
  for (const s of expect) {
    await page.waitForFunction((s) => document.documentElement.innerText.includes(s), s)
      .catch(() => {});
    const said = await page.evaluate(() => document.documentElement.innerText);
    assert(said.includes(s), `the try says it: ${said.slice(0, 3000)}`);
  }
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    test(`${engine} ${lang}: a try names the rule that would match, and what a real request meets beside it`, async (t) => {
      const { page, errors } = await open(t, engine, lang, hit);
      const w = words[lang];
      await tryRules(page, lang, w.rule, w.real);
      const said = await page.evaluate(() => document.documentElement.innerText);
      assert(said.includes(w.label), `the try's label is translated (${w.label}): ${said.slice(0, 3000)}`);
      assert(said.includes(w.button === "Try" ? "asks for “complex”" : w.rule), `the rule's condition is told: ${said.slice(0, 3000)}`);
      assert.deepEqual(errors, []);
    });
    if (lang === "en") {
      test(`${engine} en: what a try says lies across the editor, not in the label's column`, async (t) => {
        const { page, errors } = await open(t, engine, lang, hit);
        await tryRules(page, lang, words.en.rule);
        const widths = await page.evaluate(() => {
          const said = document.querySelector(".rt-gedit > .hint");
          const row = said && said.previousElementSibling;
          return { said: said.getBoundingClientRect().width, row: row.getBoundingClientRect().width };
        });
        assert(widths.said >= widths.row * 0.9, `the try's answer is as wide as the row it answers: ${JSON.stringify(widths)}`);
        assert.deepEqual(errors, []);
      });
      test(`${engine} en: a try where no rule would match says so`, async (t) => {
        const { page, errors } = await open(t, engine, lang, none);
        await tryRules(page, lang, words.en.noRule, words.en.real);
        assert.deepEqual(errors, []);
      });
      test(`${engine} en: a group that decides nothing says so`, async (t) => {
        const { page, errors } = await open(t, engine, lang, null);
        await tryRules(page, lang, words.en.nothing, words.en.real);
        assert.deepEqual(errors, []);
      });
    }
  }
}
