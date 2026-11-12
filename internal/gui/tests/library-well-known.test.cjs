// Website-installed skills keep an online source link and an Update action,
// in both browser engines and every language. No live gateway is contacted.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const home = "/Users/example";
const source = "https://uizze.sh";
const library = () => ({
  dir: `${home}/.config/magpie/library`, backups: `${home}/.config/magpie/backups`, home,
  agents: [{ id: "claude", name: "Claude Code", skills: `${home}/.claude/skills`, mcp: `${home}/.claude.json` }],
  instructions: { agents: [], sets: [] }, servers: [], projects: [], foundServers: [], foundSkills: [],
  skills: [{ name: "ui-taste", description: "UI taste", kind: "well-known", source, repo: "uizze.sh", agents: [] }],
});

function serve(lang, posts, data = library(), opened = []) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: data });
    if (url.pathname === "/api/open") {
      opened.push(req.postDataJSON().url);
      return route.fulfill({ json: {} });
    }
    if (url.pathname === "/api/library/skills/update") {
      posts.push(req.postDataJSON());
      return route.fulfill({ json: { ...data, result: { updated: ["ui-taste"], changed: [], problems: [] } } });
    }
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    return route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: website skills have a source link and can be updated`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of (process.env.GUI_LANG ? [process.env.GUI_LANG] : ["en", "zh", "zh-TW", "ja", "de"])) {
      for (const width of [980, 420]) {
        await t.test(`${lang} ${width}`, async () => {
          const context = await browser.newContext({ viewport: { width, height: 800 } });
          t.after(() => context.close());
          await context.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
          const page = await context.newPage();
          page.setDefaultTimeout(5000);
          const errors = [], posts = [];
          page.on("pageerror", (error) => errors.push(error.message));
          await page.route("**/*", serve(lang, posts));
          await page.goto("http://magpie.test/");
          await page.locator('button[data-view="library"]').click();
          const row = page.locator("#view-library .lib-skill");
          await row.waitFor().catch(async (error) => {
            console.log({ errors, library: await page.locator("#view-library").innerText() });
            throw error;
          });
          assert.equal(await row.locator("a.lib-srclink").count(), 1, "a website has an online source link");
          assert.equal(await row.locator("a.lib-srclink").getAttribute("href"), source, "a website is an online source, not a linked local folder");
          const update = row.locator('button[title*="uizze.sh"]');
          assert.equal(await update.count(), 1, "the installed website skill has an Update action");
          await update.click();
          await page.waitForFunction(() => !document.querySelector(".lib-rowacts.busy"));
          assert.deepEqual(posts, [{ name: "ui-taste" }]);
          assert.deepEqual(errors, []);
          await context.close();
        });
      }
    }
  });

  test(`${engine}: website groups preserve HTTP origins and ports`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const context = await browser.newContext({ viewport: { width: 980, height: 800 } });
    t.after(() => context.close());
    await context.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
    const origins = ["http://localhost:8080/kit", "http://example.com:8080/kit", "http://example.com:8081/kit", "https://example.com:8443/Kit"];
    const data = library(), opened = [];
    data.skills = origins.map((address, i) => ({ name: `website-${i}`, description: "Website skill", kind: "well-known", source: address, repo: address, agents: [] }));
    const page = await context.newPage();
    page.setDefaultTimeout(5000);
    await page.route("**/*", serve("en", [], data, opened));
    await page.goto("http://magpie.test/");
    await page.locator('button[data-view="library"]').click();
    const groups = page.locator("#view-library .lib-group");
    await groups.first().waitFor();
    assert.equal(await groups.count(), origins.length, "different origins must remain different groups");
    for (const address of origins) {
      const group = groups.filter({ has: page.getByText(address, { exact: true }) });
      await Promise.all([
        page.waitForResponse((response) => new URL(response.url()).pathname === "/api/open"),
        group.locator(".lib-grouphead .lib-rowacts > button.lib-icon").click(),
      ]);
    }
    assert.deepEqual(opened, origins, "opening a website group must preserve its scheme, host, port and path");
  });
}
