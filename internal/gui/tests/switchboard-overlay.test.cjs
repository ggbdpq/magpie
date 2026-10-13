// Run with `node --test internal/gui/tests/switchboard-overlay.test.cjs`; no
// browser is needed. The switchboard's own elements are in the page in both
// layouts (index.html), and the inspector is `position: fixed`. With the
// layout off they must take no room and catch no click: the stage, the
// inspector and the header's switch are all out of the classic page, so a
// button anywhere under them is still the one a click reaches.
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const { test } = require("node:test");

const assets = path.resolve(__dirname, "../assets");
const read = (f) => fs.readFileSync(path.join(assets, f), "utf8");

test("the switchboard's elements are born hidden in the page", () => {
  const html = read("index.html");
  for (const id of ["view-switchboard", "sbInspector", "layoutRow"]) {
    const tag = html.match(new RegExp(`<[^>]*id="${id}"[^>]*>`));
    assert.ok(tag, `${id} is in the page`);
    assert.match(tag[0], /\bhidden\b/, `${id} is born hidden`);
  }
});

test("hidden takes an element out of the layout, so nothing fixed can catch a click", () => {
  assert.match(read("app.css"), /^\[hidden\] \{ display: none !important; \}$/m);
});

test("bridge.css asks for room only under the layout's own class", () => {
  // every rule that places or sizes something must be scoped to
  // body.switchboard or an .sb-* class the shell makes; a bare selector
  // would reach the classic page too
  const css = read("bridge.css").replace(/\/\*[\s\S]*?\*\//g, "");
  for (const block of css.split("}")) {
    const at = block.lastIndexOf("{");
    if (at < 0) continue;
    const selectors = block.slice(0, at).split("\n").pop().trim();
    if (!selectors || selectors.startsWith("@")) continue;
    for (const one of selectors.split(",")) {
      const s = one.trim();
      if (!s) continue;
      assert.ok(/^(body\.switchboard|\.sb-|#view-switchboard|#sbInspector)/.test(s),
        `${s} is not scoped to the switchboard`);
    }
  }
});
