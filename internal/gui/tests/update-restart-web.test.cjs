// Run with Node's test runner and Playwright on the module path; see README.md.
// #7612 (bbingz): in magpie web (and desktop GUI for the pill/row states),
// update-restart feedback and reload survive every transition:
// 1. Header #update pill stays visible when "Waiting to update" enters
//    waitRecheck (checking/downloading + waiting), then reloads via backAsNew
//    once Reexec closes the port.
// 2. Header #update "Restart now" (when: "now") keeps polling when the backend
//    returns checking/downloading + waiting, then reloads via backAsNew.
// 3. Settings -> Version row reloads via servedAnew when again() receives a
//    non-null response from the new version after a fast Reexec.
// 4. Settings -> Version row install() disables buttons, shows busy text,
//    guards double-clicks, and stops again(1000) from regressing the row mid-
//    recheck.
// 5. Connection abort on POST /api/update/install (header pill and Settings
//    row) still triggers backAsNew and reloads into the new version.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = strings();
const tr = (lang, k, v = {}) => ((lang === "en" ? k : I18N[lang][k]) || k).replace(/\{(\w+)\}/g, (_, n) => v[n]);

function settingsPayload(lang, version) {
  return {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version, dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
  };
}

function server(lang, ctl) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/") ctl.pages++;
    const cur = settingsPayload(lang, ctl.version);
    if (url.pathname === "/boot.js") {
      return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true,version:"${ctl.version}"};` });
    }
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: cur, fx: cur.fx });
    if (url.pathname === "/api/settings") return json(cur);
    if (url.pathname === "/api/update") {
      const next = ctl.onUpdate(url);
      if (next === "gone") return route.abort("connectionrefused");
      return json(next);
    }
    if (url.pathname === "/api/update/install") {
      const body = req.postDataJSON() || {};
      ctl.installs.push(body);
      const ans = await ctl.onInstall(body);
      if (ans === "abort") return route.abort("connectionrefused");
      if (ans === 204) return route.fulfill({ status: 204 });
      return json(ans);
    }
    if (url.pathname === "/api/update/notes") return json({ releases: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png", ".json": "application/json" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const confirmAsk = (page) => page.locator("#modal .update-ask button.primary").click();

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": web update-restart pill, Version row and reload transitions (#7612)", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());

    const open = async (lang, ctl, query, width = 900) => {
      const errors = [];
      const page = await (await browser.newContext({ viewport: { width, height: 600 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(8000);
      page.setDefaultNavigationTimeout(60000);
      page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message); });
      await page.route("**/*", server(lang, ctl));
      await watchSettingsReads(page);
      await page.goto("http://magpie.test/" + query);
      if (query.includes("settings")) await settingsDrawn(page);
      await page.evaluate(() => { window.oldPage = true; });
      return { page, errors };
    };

    for (const lang of ["en", "zh"]) {
      await t.test(lang + ": header pill survives waiting -> checking/downloading -> gone and reloads", async () => {
        let phase = 0;
        const ctl = {
          version: "0.1.400",
          pages: 0,
          installs: [],
          onUpdate: () => {
            if (ctl.version === "0.1.401") return { state: "latest", current: "0.1.401", latest: "0.1.401" };
            if (phase === 0) return { state: "ready", current: "0.1.400", latest: "0.1.401", busy: { requests: 1 } };
            if (phase === 1) { phase = 2; return { state: "ready", current: "0.1.400", latest: "0.1.401", waiting: true, busy: { requests: 1 } }; }
            if (phase === 2) { phase = 3; return { state: "checking", current: "0.1.400", latest: "0.1.401", waiting: true }; }
            if (phase === 3) { phase = 4; return { state: "downloading", current: "0.1.400", latest: "0.1.402", waiting: true, done: 5e6, total: 2e7 }; }
            if (phase === 4) { phase = 5; return "gone"; }
            ctl.version = "0.1.401";
            return { state: "latest", current: "0.1.401", latest: "0.1.401" };
          },
          onInstall: () => {
            phase = 1;
            return { state: "ready", current: "0.1.400", latest: "0.1.401", waiting: true, busy: { requests: 1 } };
          },
        };
        const { page, errors } = await open(lang, ctl, "?mode=panel", 420);
        const pill = page.locator("#update"), label = pill.locator("span").first();
        await pill.waitFor({ state: "visible" });
        await pill.click();
        await page.waitForFunction((txt) => document.querySelector("#update span")?.textContent.trim() === txt, tr(lang, "Waiting to update"));
        // Pill must stay visible through checking -> downloading -> gone and reload into 0.1.401.
        await page.waitForFunction(() => !document.querySelector("#update").hidden && /25%/.test(document.querySelector("#update span")?.textContent || ""), null, { timeout: 6000 });
        await page.waitForFunction(() => !window.oldPage && document.readyState === "complete", null, { timeout: 6000 });
        assert.equal(ctl.pages, 2);
        assert.deepEqual(errors, []);
        await page.context().close();
      });

      await t.test(lang + ": header pill Restart now stays visible on checking/downloading and reloads", async () => {
        let phase = 0;
        const ctl = {
          version: "0.1.400",
          pages: 0,
          installs: [],
          onUpdate: () => {
            if (ctl.version === "0.1.401") return { state: "latest", current: "0.1.401", latest: "0.1.401" };
            if (phase === 0) return { state: "ready", current: "0.1.400", latest: "0.1.401", waiting: true, busy: { requests: 1 } };
            if (phase === 1) { phase = 2; return { state: "downloading", current: "0.1.400", latest: "0.1.402", waiting: true, done: 1e7, total: 2e7 }; }
            if (phase === 2) { phase = 3; return "gone"; }
            ctl.version = "0.1.401";
            return { state: "latest", current: "0.1.401", latest: "0.1.401" };
          },
          onInstall: () => {
            phase = 1;
            return { state: "checking", current: "0.1.400", latest: "0.1.401", waiting: true };
          },
        };
        const { page, errors } = await open(lang, ctl, "?mode=panel", 420);
        const pill = page.locator("#update");
        await page.waitForFunction((txt) => document.querySelector("#update span")?.textContent.trim() === txt, tr(lang, "Waiting to update"));
        await pill.click();
        await page.waitForFunction(() => !document.querySelector("#update").hidden && /50%/.test(document.querySelector("#update span")?.textContent || ""), null, { timeout: 6000 });
        await page.waitForFunction(() => !window.oldPage && document.readyState === "complete", null, { timeout: 6000 });
        assert.deepEqual(ctl.installs, [{ when: "now" }]);
        assert.equal(ctl.pages, 2);
        assert.deepEqual(errors, []);
        await page.context().close();
      });

      await t.test(lang + ": Settings Version row reloads via servedAnew on fast Reexec through again()", async () => {
        let armed = false;
        const ctl = {
          version: "0.1.400",
          pages: 0,
          installs: [],
          onUpdate: () => {
            if (ctl.version === "0.1.401") return { state: "latest", current: "0.1.401", latest: "0.1.401" };
            if (armed) {
              ctl.version = "0.1.401";
              return { state: "latest", current: "0.1.401", latest: "0.1.401" };
            }
            return { state: "ready", current: "0.1.400", latest: "0.1.401", waiting: true, busy: { requests: 1 } };
          },
          onInstall: () => ({ state: "ready", current: "0.1.400", latest: "0.1.401", waiting: true }),
        };
        const { page, errors } = await open(lang, ctl, "?view=settings&tab=about", 900);
        // Disable header pill polling so only Settings -> Version row's again(1000) fires.
        await page.evaluate(() => { clearTimeout(document.querySelector("#update")?.waitPoll); });
        const row = page.locator("#about .row.pref", { has: page.locator(".name", { hasText: tr(lang, "Version") }) }).first();
        await row.locator("button", { hasText: tr(lang, "Restart now") }).waitFor();
        armed = true;
        await page.waitForFunction(() => !window.oldPage && document.readyState === "complete", null, { timeout: 3000 });
        assert.equal(ctl.pages, 2);
        assert.deepEqual(errors, []);
        await page.context().close();
      });

      await t.test(lang + ": Settings install(now) disables buttons and blocks again(1000) regression mid-recheck", async () => {
        let midRecheck = false;
        const ctl = {
          version: "0.1.400",
          pages: 0,
          installs: [],
          onUpdate: () => {
            if (midRecheck) {
              // Mirrors update.go:1050 cancelWait() before recheck(true): waiting is false mid-recheck.
              return { state: "ready", current: "0.1.400", latest: "0.1.401", waiting: false };
            }
            return { state: "ready", current: "0.1.400", latest: "0.1.401", waiting: true, busy: { requests: 1 } };
          },
          onInstall: async () => {
            midRecheck = true;
            await new Promise((r) => setTimeout(r, 1400));
            return { state: "checking", current: "0.1.400", latest: "0.1.401", waiting: true };
          },
        };
        const { page, errors } = await open(lang, ctl, "?view=settings&tab=about", 900);
        const row = page.locator("#about .row.pref", { has: page.locator(".name", { hasText: tr(lang, "Version") }) }).first();
        const nowBtn = row.locator("button", { hasText: tr(lang, "Restart now") });
        await nowBtn.waitFor();
        await nowBtn.click();
        // Immediately disabled and showing Restarting…, ignoring a second click.
        assert.equal(await nowBtn.isDisabled(), true, "Restart now should be disabled while POST is in flight");
        assert.equal((await row.locator(".sub").textContent()).trim(), tr(lang, "Restarting…"));
        // Wait past again(1000) while POST is still pending: row must NOT regress to "Restart to update".
        await page.waitForTimeout(1150);
        const btns = await row.locator(".val button").allTextContents();
        assert.ok(!btns.includes(tr(lang, "Restart to update")), `row regressed mid-recheck: ${btns}`);
        await page.waitForFunction((txt) => [...document.querySelectorAll("#about .row.pref .sub")].some((s) => s.textContent.trim() === txt), tr(lang, "Checking for updates…"));
        assert.equal(ctl.installs.length, 1);
        assert.deepEqual(errors, []);
        await page.context().close();
      });

      await t.test(lang + ": POST /api/update/install connection abort still triggers backAsNew reload", async () => {
        for (const surface of ["pill", "settings"]) {
          let abortAt = 0;
          const ctl = {
            version: "0.1.400",
            pages: 0,
            installs: [],
            onUpdate: () => {
              if (!abortAt) return { state: "ready", current: "0.1.400", latest: "0.1.401" };
              // Server stays down for 600ms during Reexec before answering as 0.1.401.
              if (Date.now() - abortAt < 600) return "gone";
              ctl.version = "0.1.401";
              return { state: "latest", current: "0.1.401", latest: "0.1.401" };
            },
            onInstall: () => {
              abortAt = Date.now();
              return "abort";
            },
          };
          const query = surface === "pill" ? "?mode=panel" : "?view=settings&tab=about";
          const { page, errors } = await open(lang, ctl, query, surface === "pill" ? 420 : 900);
          if (surface === "pill") {
            await page.locator("#update").waitFor({ state: "visible" });
            await page.locator("#update").click();
          } else {
            const row = page.locator("#about .row.pref", { has: page.locator(".name", { hasText: tr(lang, "Version") }) }).first();
            await row.locator("button", { hasText: tr(lang, "Restart to update") }).click();
            await confirmAsk(page);
          }
          await page.waitForFunction(() => !window.oldPage && document.readyState === "complete", null, { timeout: 4000 });
          assert.equal(ctl.pages, 2, `${surface}: page reloaded after install abort`);
          assert.deepEqual(errors, []);
          await page.context().close();
        }
      });
    }
  });
}
