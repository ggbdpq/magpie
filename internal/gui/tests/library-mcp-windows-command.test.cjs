// Run with Node's test runner and Playwright on the module path; see README.md.
// #2685 (ufec): on Windows, a command typed into the Library's MCP editor as
// an absolute path — `C:\Users\me\...\npx.cmd -y pkg` — saved as
// `C:Usersme...npx.cmd`: words() took every `\` as an escape, and a `\` in a
// Windows path is a separator, so both the colon and the separators vanished
// and the server never started ("cannot find npx"). A double-quoted path lost
// its separators the same way ("C:\Program Files\..."); only a single-quoted
// one survived, and a UNC path (\\srv\share) lost its leading `\\`. The line
// is split for argv, not for a shell: a backslash now takes only the next
// blank or quote with it, so a path keeps its separators while `my\ server`
// still means one word with a space. Saved through the real page, API faked
// here, in en and zh on Chromium.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/xv";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });

function server(lang, saves) {
  const lib = {
    dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
    agents: [agent("claude", "Claude Code")],
    instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [],
    servers: [{ name: "local", transport: "stdio", command: "npx", args: ["-y", "pkg"], agents: ["claude"] }],
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib });
    if (url.pathname === "/api/library/mcp/check") return route.fulfill({ json: { servers: {} } });
    if (url.pathname === "/api/library/servers/save") {
      const { server: s } = req.postDataJSON();
      saves.push({ command: s.command, args: s.args });
      const i = lib.servers.findIndex((x) => x.name === s.name);
      lib.servers[i] = { ...lib.servers[i], ...s };
      return route.fulfill({ json: { ...lib, result: { changed: ["claude"] } } });
    }
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a Windows path typed in the MCP command field saves whole", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const { I18N } = strings();
        const w = (s) => (lang === "en" ? s : I18N[lang][s]) || s;
        const saves = [];
        const ctx = await browser.newContext({ viewport: { width: 1100, height: 800 } });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, saves));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const row = page.locator("#view-library .lib-server", { has: page.locator('.lib-health[data-server="local"]') });
        const ed = page.locator("#modal .lib-editor");
        const line = ed.locator("input.mono").first();
        const save = async () => {
          await ed.locator("> .bar button.primary", { hasText: new RegExp("^" + w("Save") + "$") }).click();
          await ed.waitFor({ state: "detached" });
        };
        // the reporter's line, byte for byte
        const typed = "C:\\Users\\xxx\\AppData\\Roaming\\fnm\\aliases\\default\\npx.cmd -y chrome-devtools-mcp@latest";
        const want = "C:\\Users\\xxx\\AppData\\Roaming\\fnm\\aliases\\default\\npx.cmd";

        await row.locator(".name").click();
        await ed.waitFor();
        await line.fill(typed);
        await save();
        assert.deepEqual(saves.pop(), { command: want, args: ["-y", "chrome-devtools-mcp@latest"] }, "the reporter's path");

        // reopening shows the saved server; saving it again keeps it whole
        // (quote()'s single quotes are word-true in words())
        await row.locator(".name").click();
        await ed.waitFor();
        await save();
        assert.deepEqual(saves.pop(), { command: want, args: ["-y", "chrome-devtools-mcp@latest"] }, "a saved path survives its own editor");

        // a path with a blank, double-quoted, keeps its separators too
        await row.locator(".name").click();
        await ed.waitFor();
        await line.fill('"C:\\Program Files\\nodejs\\npx.cmd" -y pkg');
        await save();
        assert.deepEqual(saves.pop(), { command: "C:\\Program Files\\nodejs\\npx.cmd", args: ["-y", "pkg"] }, "a double-quoted path");

        // one that ends in a backslash, the way a folder separator leaves
        // it: the quote still closes (`\` takes nothing with it in there),
        // and the saved path survives its own editor
        await row.locator(".name").click();
        await ed.waitFor();
        await line.fill('"C:\\Program Files\\" -y pkg');
        await save();
        assert.deepEqual(saves.pop(), { command: "C:\\Program Files\\", args: ["-y", "pkg"] }, "a double-quoted path ending in a backslash");
        await row.locator(".name").click();
        await ed.waitFor();
        await save();
        assert.deepEqual(saves.pop(), { command: "C:\\Program Files\\", args: ["-y", "pkg"] }, "a trailing separator survives its own editor");

        // a UNC path keeps its leading double separator
        await row.locator(".name").click();
        await ed.waitFor();
        await line.fill("\\\\srv\\share\\svc.exe -l 8");
        await save();
        assert.deepEqual(saves.pop(), { command: "\\\\srv\\share\\svc.exe", args: ["-l", "8"] }, "a UNC path");

        // a POSIX escape still means a blank inside one word
        await row.locator(".name").click();
        await ed.waitFor();
        await line.fill("my\\ server -y pkg");
        await save();
        assert.deepEqual(saves.pop(), { command: "my server", args: ["-y", "pkg"] }, "a POSIX-escaped blank");
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
