// The blueprint configurator: assemble a robot out of components and see, while
// you do it, what you have built.
//
// Nothing on this page is decided here. The §6.3 constraints, the balance
// tables, the derived speed and health, the consequence sentences and the
// verdict on every program in the library all come from the server — this file
// only lays them out. E7.3 retunes the game by editing internal/sim's tables,
// and a number or a rule copied into this file is a page that starts lying the
// day it is.
//
// The one thing it does draw from its own knowledge is the silhouette, and only
// because the shape strings are shared with the arena renderer (shapes.js) and
// its dimensions — sight and radar reach — arrive on the preview payload.

import { SHAPES, MUZZLE, slug } from "./shapes.js";

const $ = (id) => document.getElementById(id);
const SVGNS = "http://www.w3.org/2000/svg";

function el(tag, props = {}, ...kids) {
  const n = document.createElement(tag);
  Object.assign(n, props);
  n.append(...kids.filter((k) => k));
  return n;
}
function svg(tag, attrs = {}) {
  const n = document.createElementNS(SVGNS, tag);
  for (const [k, v] of Object.entries(attrs)) n.setAttribute(k, v);
  return n;
}
const err = (m) => { $("err").textContent = m || ""; };
const status = (m) => { $("status").textContent = m || ""; };

async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: body ? { "Content-Type": "application/json" } : {},
    body: body ? JSON.stringify(body) : undefined,
  });
  if (res.status === 401) { location.href = "/login"; return null; }
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

let lang = null;        // /api/language: the component catalogue and the limits
let blueprints = [];    // the saved library
let picked = [];        // the design on screen, as variant ids in slot order
let preview = null;     // the last /api/blueprints/preview answer
let previewFor = "";    // the parts list that answer describes
let timer = null;

const key = () => picked.join(",");
const comp = (v) => lang.components.find((c) => c.variant === v) || { variant: v, name: `#${v}`, kind: "unknown", mass: 0, value: 0 };
const kindColor = (kind) => `var(--kind-${slug(kind)}, var(--kind-unknown))`;

// kinds is the catalogue's own kind list, in catalogue order, so a component
// kind added server-side gets a bay and a palette group with no change here.
const kinds = () => [...new Set(lang.components.map((c) => c.kind))];

// ---------------------------------------------------------------------------
// Bays — what is installed, one slot at a time
// ---------------------------------------------------------------------------

function renderBays() {
  const host = $("bays");
  host.replaceChildren();
  for (const kind of kinds()) {
    const slots = picked.map((v, i) => [v, i]).filter(([v]) => comp(v).kind === kind);
    if (slots.length === 0) {
      host.append(emptyBay(kind));
      continue;
    }
    for (const [v, i] of slots) host.append(bay(v, i));
  }
}

function bay(variant, index) {
  const c = comp(variant);
  const sw = el("span", { className: "swatch" });
  sw.style.background = kindColor(c.kind);
  const drop = el("button", { textContent: "✕", title: `remove ${c.name}` });
  drop.addEventListener("click", () => { picked.splice(index, 1); refresh(); });
  return el("div", { className: "bay" }, sw,
    el("div", { className: "body" },
      el("span", { className: "kind", textContent: c.kind }),
      el("span", { className: "what", textContent: c.name }),
      el("span", { className: "cost", textContent: `${c.value} budget · ${c.mass} mass` })),
    drop);
}

// An empty bay is drawn rather than omitted: a slot you have not spent is a
// decision the player is making, and the cheapest part that would fill it is
// the fact that makes it one.
function emptyBay(kind) {
  const cheapest = lang.components.filter((c) => c.kind === kind)
    .reduce((a, b) => (a && a.value <= b.value ? a : b), null);
  return el("div", { className: "bay empty" },
    el("span", { className: "swatch" }),
    el("div", { className: "body" },
      el("span", { className: "kind", textContent: `${kind} · empty` }),
      el("span", { className: "what", textContent: cheapest
        ? `${cheapest.name} is the cheapest fit, at ${cheapest.value} budget and ${cheapest.mass} mass`
        : "nothing in the catalogue fits" })));
}

function renderPalette() {
  const host = $("palette");
  host.replaceChildren();
  for (const kind of kinds()) {
    host.append(el("span", { className: "kindname", textContent: kind }));
    for (const c of lang.components.filter((x) => x.kind === kind)) {
      const b = el("button", { textContent: `+ ${c.name}`, title: `${c.value} budget · ${c.mass} mass` });
      b.addEventListener("click", () => { picked.push(c.variant); refresh(); });
      host.append(b);
    }
  }
}

// ---------------------------------------------------------------------------
// The two meters
//
// Budget is the whole trade this page exists for, so the bar is the *starting
// budget* and not this one robot: what a player is really choosing is how many
// robots they open with, and a bar showing one design's cost in isolation
// cannot say that. Each block is one robot the budget fields, split into its
// parts; what is left over is the base inventory it becomes instead.
// ---------------------------------------------------------------------------

function renderBudget() {
  const host = $("budget");
  host.replaceChildren();
  if (!preview) return;
  const { value, budget, fleet } = preview;
  const track = el("div", { className: "track" });
  for (let n = 0; n < fleet; n++) {
    const unit = el("div", { className: "unit" });
    unit.style.width = `${(value / budget) * 100}%`;
    for (const v of picked) {
      const c = comp(v);
      const seg = el("span", { className: "seg", title: `${c.name} — ${c.value}` });
      seg.style.width = `${(c.value / value) * 100}%`;
      seg.style.background = kindColor(c.kind);
      unit.append(seg);
    }
    track.append(unit);
  }
  host.append(
    el("div", { className: "head" },
      el("span", { textContent: `${value} each` }),
      el("b", { textContent: `${fleet} × ${value} / ${budget}` })),
    track,
    // What the picture means, not what it costs — the consequence column says
    // that, and saying it twice in different words is how two answers drift.
    el("div", { className: "meta", textContent: preview.ok
      ? `One block per robot the starting budget opens with, split by the part each point went on.`
      : "Legal designs only: the budget cannot field something §6.3 refuses to build." }),
  );
}

// Ticks per cell is a count, so it is drawn as that many blocks. A proportional
// bar would need a scale — the speed of the fastest legal robot — and that is a
// balance number this file is not allowed to know.
function renderSpeed() {
  const host = $("speed");
  host.replaceChildren();
  if (!preview) return;
  const t = preview.ticks_per_cell;
  const ticks = el("div", { className: "ticks" });
  for (let i = 0; i < Math.min(t, 16); i++) ticks.append(el("i"));
  host.append(
    el("div", { className: "head" },
      el("span", { textContent: `${preview.mass} mass` }),
      el("b", { textContent: `1 cell / ${t} tick${t === 1 ? "" : "s"}` })),
    ticks,
    // One block is one tick. No proportional bar and no seconds: the arena size
    // and the tick rate are the match's, not this page's, and a sentence that
    // assumed either would be wrong on the first lobby that changed one.
    el("div", { className: "meta", textContent:
      `Speed ${preview.speed} on open ground. Every part you add is mass, and mass is blocks.` }),
  );
}

// ---------------------------------------------------------------------------
// Consequences and program fit — both straight off the wire
// ---------------------------------------------------------------------------

function renderConsequences() {
  const host = $("consequences");
  host.replaceChildren();
  if (!preview) return;
  if (!preview.ok) {
    host.append(el("p", { className: "meta", textContent: preview.error }));
    return;
  }
  for (const line of preview.consequences || []) host.append(el("p", { textContent: line }));
}

function renderFit() {
  const host = $("fit");
  host.replaceChildren();
  if (!preview || !preview.ok) {
    host.append(el("span", { className: "meta", textContent: "—" }),
      el("span", { className: "meta", textContent: "Finish the design and every program in your library is checked against it." }));
    return;
  }
  const list = preview.programs || [];
  if (list.length === 0) {
    host.append(el("span", { className: "meta", textContent: "—" }),
      el("span", { className: "meta" }, el("a", { href: "/editor", textContent: "Your library has no programs yet." })));
    return;
  }
  for (const p of list) {
    host.append(el("span", { className: p.ok ? "" : "no", textContent: p.ok ? "✓" : "✗" }));
    const line = el("span", { className: p.ok ? "" : "no" }, el("span", { textContent: p.name }));
    if (!p.ok) line.append(el("span", { className: "why", textContent: ` — ${p.blocked}` }));
    else if (p.dead > 0) line.append(el("span", { className: "why", textContent: ` — ${p.dead} dead rule${p.dead === 1 ? "" : "s"}: hardware this design does not carry` }));
    host.append(line);
  }
}

// ---------------------------------------------------------------------------
// Silhouette
//
// Drawn from shapes.js — the same path strings the arena fills — and from the
// reach numbers on the preview payload, so what a player designs against is
// what the match will show them. Both reaches are Chebyshev, which is why the
// radar is a square and not the ring it looks like it ought to be: a circle
// here would promise contacts at the corners that the simulation never reports.
// ---------------------------------------------------------------------------

function renderSilhouette() {
  const host = $("silhouette");
  host.replaceChildren();
  if (!preview || !preview.ok) {
    $("sil-desc").textContent = "";
    host.setAttribute("aria-label", "no silhouette: the design is not legal yet");
    return;
  }
  const sight = preview.sight, radar = preview.radar;
  const reach = Math.max(sight, radar) + 1;
  host.setAttribute("viewBox", `${-reach} ${-reach} ${reach * 2} ${reach * 2}`);

  if (radar > 0) {
    host.append(svg("rect", { class: "ring", x: -radar, y: -radar, width: radar * 2, height: radar * 2 }));
  }
  // Forward vision, design §7.1: Chebyshev range and a ±45° cone facing the
  // nose, which is exactly the triangle below — |dx| ≤ |dy| ≤ range.
  const wedge = `0,0 ${-sight},${-sight} ${sight},${-sight}`;
  host.append(svg("polygon", { class: "wedge", points: wedge }));
  host.append(svg("polygon", { class: "wedge-edge", points: wedge }));

  const loco = picked.map(comp).find((c) => c.kind === "locomotion");
  const shape = loco && SHAPES[slug(loco.name)] ? slug(loco.name) : "unknown";
  const armed = picked.map(comp).some((c) => c.kind === "weapon");
  // The hull is drawn about three cells across rather than the one it occupies.
  // At true scale a robot inside a 28-cell radar square is four pixels, and the
  // panel's job is to show which chassis and whether there is a barrel — the
  // arena is where a robot is drawn to size.
  const g = svg("g", { transform: `scale(${reach / 8})` });
  if (armed) {
    const barrel = svg("path", { class: "hull", d: MUZZLE });
    barrel.style.fill = "var(--kind-weapon)";
    g.append(barrel);
  }
  const hull = svg("path", { class: "hull", d: SHAPES[shape] });
  hull.style.fill = "var(--colony-0)";
  g.append(hull);
  host.append(g);

  const bits = [
    `the ${loco ? loco.name : "unknown"} chassis is the shape teammates read it by`,
    armed ? "a barrel past the nose: every other player can see it is armed"
          : "no barrel: it reads as unarmed to every other player",
    `the wedge is its sight, 90° and ${sight} cells`,
    radar > 0 ? `the square is radar reach, ${radar} cells in every direction`
              : "no radar, so nothing outside the wedge exists to it",
  ];
  $("sil-desc").textContent = `${bits.join("; ")}.`;
  host.setAttribute("aria-label", $("sil-desc").textContent);
}

// ---------------------------------------------------------------------------
// Preview: one debounced round trip per edit, and every panel redrawn from it
// ---------------------------------------------------------------------------

function refresh() {
  renderBays();
  renderBudget();
  renderSpeed();
  renderConsequences();
  renderFit();
  renderSilhouette();
  $("save").disabled = !(preview && preview.ok && previewFor === key());
  clearTimeout(timer);
  timer = setTimeout(async () => {
    const k = key();
    try {
      const res = await api("POST", "/api/blueprints/preview", { components: picked });
      // Answers can land out of order; one for a design the player has already
      // moved on from is dropped rather than shown.
      if (!res || k !== key()) return;
      preview = res;
      previewFor = k;
      err("");
      refresh();
    } catch (e) { err(e.message); }
  }, 150);
}

function load(bp) {
  picked = [...bp.components];
  $("name").value = `${bp.name} mk2`;
  preview = null;
  previewFor = "";
  status("");
  refresh();
}

function renderLibrary() {
  const sel = $("library");
  const keep = sel.value;
  sel.replaceChildren(el("option", { value: "", textContent: `${blueprints.length} saved…` }));
  for (const b of blueprints) sel.append(el("option", { value: String(b.id), textContent: b.name }));
  if (blueprints.some((b) => String(b.id) === keep)) sel.value = keep;
}

const selected = () => blueprints.find((b) => String(b.id) === $("library").value);

// ---------------------------------------------------------------------------
// Wiring
// ---------------------------------------------------------------------------

$("library").addEventListener("change", () => { const b = selected(); if (b) load(b); });
$("copy").addEventListener("click", () => { const b = selected(); if (b) load(b); });
$("clear").addEventListener("click", () => {
  picked = []; preview = null; previewFor = ""; $("name").value = ""; status(""); refresh();
});

// There is no PUT. An approved blueprint is immutable (design §5.1) — robots
// already built to it are still walking around — so editing one saves a copy.
$("save").addEventListener("click", async () => {
  err(""); status("");
  try {
    const bp = await api("POST", "/api/blueprints", { name: $("name").value, components: picked });
    if (!bp) return;
    blueprints.push(bp);
    renderLibrary();
    $("library").value = String(bp.id);
    status(`Saved. Approve it on the lobbies page and your base will build it.`);
  } catch (e) { err(e.message); }
});

(async function start() {
  try {
    lang = await api("GET", "/api/language");
    if (!lang) return;
    $("name").maxLength = lang.limits.max_name_len;
    blueprints = (await api("GET", "/api/blueprints")).blueprints;
    renderLibrary();
    renderPalette();
    if (blueprints.length > 0) {
      $("library").value = String(blueprints[0].id);
      load(blueprints[0]);
    } else {
      refresh();
    }
  } catch (e) { err(e.message); }
})();
