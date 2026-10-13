// shell.js — the switchboard shell (接线台): the stage page, the header's
// Switchboard / Insights switch, the inspector's container and the layout
// pick in Settings › General. Loaded before app.js so its body class is on
// before app.js lays the navigation out; app.js calls magpieShell.boot()
// at the end of its own boot. Everything visual keys off body.switchboard,
// so a classic load is the page it was, byte for byte. layoutOf and
// redirectOf are pure and run in Node (switchboard-shell.test.cjs's seam).
(function (root) {
  const LAYOUT_KEY = "magpie.layout";

  // layoutOf: which layout a load gets. The switchboard stays opt-in until
  // it carries everything the classic pages do; a `layout` in the address
  // wins over the kept pick, for a link or a one-off look. The tray panel
  // keeps its tabs until its own switchboard (the design's P3).
  function layoutOf(search, stored, mode) {
    if (mode === "panel") return "classic";
    const q = new URLSearchParams(search || "").get("layout");
    if (q === "switchboard" || q === "classic") return q;
    return stored === "switchboard" ? "switchboard" : "classic";
  }

  // redirectOf: where an old address lands in the switchboard. The stage
  // is the landing page; Sessions, the Library and a request's story move
  // into the inspector's tabs; Insights is the Usage page; Settings and
  // Plugins keep pages of their own.
  function redirectOf(search, hash) {
    const q = new URLSearchParams(search || "");
    let v = q.get("view") || "";
    if (!v && hash) v = String(hash).replace(/^#/, "");
    switch (v) {
      case "":
      case "agents":
      case "switchboard":
        return { page: "switchboard", inspect: null };
      case "providers":
      case "gateway": {
        const out = { page: "switchboard", inspect: null };
        if (q.get("edit")) out.edit = q.get("edit");
        return out;
      }
      case "sessions": {
        const out = { page: "switchboard", inspect: "sessions" };
        if (q.get("id")) out.id = q.get("id");
        return out;
      }
      case "library": {
        const out = { page: "switchboard", inspect: "library" };
        if (q.get("tab")) out.tab = q.get("tab");
        return out;
      }
      case "routing": {
        const out = { page: "switchboard", inspect: "requests" };
        if (q.get("req")) out.req = q.get("req");
        return out;
      }
      case "usage":
        return { page: "insights", inspect: null, tab: q.get("tab") || null };
      default: {
        return { page: v, inspect: null, tab: q.get("tab") || null };
      }
    }
  }

  let layout = "classic";
  let mode = "window";

  function storedLayout() {
    try { return localStorage.getItem(LAYOUT_KEY) || ""; } catch { return ""; }
  }

  if (typeof document !== "undefined") {
    mode = new URLSearchParams(location.search).get("mode") || "window";
    layout = layoutOf(location.search, storedLayout(), mode);
    if (layout === "switchboard" && mode !== "panel") {
      document.body.classList.add("switchboard");
      // the classic Agents page is the markup's one view not born hidden;
      // keep it out of the first paint, boot() opens the stage
      const agents = document.getElementById("view-agents");
      if (agents) agents.hidden = true;
    }
  }

  const on = () => document.body.classList.contains("switchboard");

  // the header's Switchboard / Insights switch, app.js's own segmented
  // control so it slides and reads like every other
  let seg = null;
  function buildSeg() {
    if (seg) return;
    seg = document.createElement("div");
    seg.className = "sb-seg";
    document.getElementById("crumb").after(seg);
    syncSeg();
  }
  function syncSeg() {
    if (!seg) return;
    const cur = typeof view === "undefined" ? ""
      : view === "usage" ? "insights"
      : view === "switchboard" ? "switchboard" : "";
    seg.replaceChildren(segs([
      ["switchboard", t("Switchboard")],
      ["insights", t("Insights")],
    ], cur, (id) => { show(id === "insights" ? "usage" : "switchboard"); }));
  }
  // the switch follows every show(), whoever called it
  let wrapped = false;
  function wrapShow() {
    if (wrapped || typeof root.show !== "function") return;
    wrapped = true;
    const orig = root.show;
    root.show = function () {
      const r = orig.apply(this, arguments);
      Promise.resolve(r).then((ok) => { if (ok !== false && on()) syncSeg(); });
      return r;
    };
  }

  // the stage: the two banks and the river between them, quiet until the
  // banks and bridges land (bridge.js)
  function paintStage() {
    const stage = document.getElementById("view-switchboard");
    if (!stage || stage.dataset.sb) return;
    stage.dataset.sb = "1";
    const bank = (side, title) => {
      const s = document.createElement("section");
      s.className = "sb-bank " + side;
      const h = document.createElement("h2");
      h.className = "sb-h";
      h.textContent = title;
      s.append(h);
      return s;
    };
    const river = document.createElement("div");
    river.className = "sb-river";
    const calm = document.createElement("p");
    calm.className = "sb-calm";
    calm.textContent = t("Agents on the left, model sources on the right; the bridges between them are drawn here.");
    river.append(calm);
    stage.append(bank("sb-left", t("Agents")), river, bank("sb-right", t("Model sources")));
  }

  // where a link lands; the inspector's tabs arrive with the banks, until
  // then its addressees keep their own pages
  const INSPECT_PAGE = { sessions: "sessions", library: "library", requests: "routing" };
  function go(dest) {
    if (dest.edit) { editing = dest.edit; show("providers"); return; }
    if (dest.inspect && INSPECT_PAGE[dest.inspect]) { show(INSPECT_PAGE[dest.inspect]); return; }
    show(dest.page === "insights" ? "usage" : dest.page);
  }

  // the layout pick in Settings › General, in both layouts
  function wireLayoutRow() {
    const row = document.getElementById("layoutRow"), box = document.getElementById("layoutSegs");
    if (!row || !box || box.dataset.sb) return;
    // the panel opens Settings in the window; a gateway-only machine has
    // no agents to draw bridges between
    if (mode === "panel" || document.body.classList.contains("gateway-mode")) return;
    box.dataset.sb = "1";
    row.hidden = false;
    paintLayoutRow();
  }
  function paintLayoutRow() {
    const box = document.getElementById("layoutSegs");
    if (!box || !box.dataset.sb) return;
    box.replaceChildren(segs([
      ["switchboard", t("Switchboard")],
      ["classic", t("Classic")],
    ], layout, setLayout));
  }
  function setLayout(v) {
    layout = v === "switchboard" ? "switchboard" : "classic";
    try { localStorage.setItem(LAYOUT_KEY, layout); } catch { /* this window's, then */ }
    apply();
    paintLayoutRow();
  }
  function apply() {
    const want = layout === "switchboard" && mode !== "panel" && !document.body.classList.contains("gateway-mode");
    if (want === on()) return;
    document.body.classList.toggle("switchboard", want);
    layoutNav();
    if (want) {
      buildSeg();
      wrapShow();
      paintStage();
      show("switchboard");
    } else {
      seg?.remove();
      seg = null;
      show("agents");
      fitTop();
    }
  }

  // boot: app.js calls this at the end of its own boot
  function boot() {
    if (typeof document === "undefined") return;
    wireLayoutRow();
    if (!on()) return;
    if (document.body.classList.contains("gateway-mode")) {
      document.body.classList.remove("switchboard");
      layoutNav();
      return;
    }
    buildSeg();
    wrapShow();
    paintStage();
    go(redirectOf(location.search, location.hash));
  }

  const api = {
    layoutOf, redirectOf, boot, setLayout, apply, renderSettings: paintLayoutRow,
    get layout() { return layout; },
  };
  if (typeof module !== "undefined" && module.exports) module.exports = api;
  root.magpieShell = api;
})(typeof globalThis !== "undefined" ? globalThis : this);
