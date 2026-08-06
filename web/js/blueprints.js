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
let editing = 0;        // library id the draft writes back to, 0 = a new design
let preview = null;     // the last /api/blueprints/preview answer
let previewFor = null;  // the parts list that answer describes
let hypo = {};          // kind -> the preview for "and the cheapest one of these"
let hypoFor = null;     // the parts list those answers are hypotheticals of
let timer = null;

const key = () => picked.join(",");
const comp = (v) => lang.components.find((c) => c.variant === v) || { variant: v, name: `#${v}`, kind: "unknown", mass: 0, value: 0 };
const kindColor = (kind) => `var(--kind-${slug(kind)}, var(--kind-unknown))`;

// kinds is the catalogue's own kind list, in catalogue order, so a component
// kind added server-side gets a bay and a palette group with no change here.
const kinds = () => [...new Set(lang.components.map((c) => c.kind))];

// Display vocabulary, and only that. The catalogue's kind is the wire value and
// every request still carries it; "radar" is simply called SENSOR on screen,
// which is the word design 1f puts on the bay. Never write one of these back.
const KIND_LABEL = { radar: "sensor" };
const kindLabel = (kind) => KIND_LABEL[kind] || kind;

const cheapestOf = (kind) => lang.components.filter((c) => c.kind === kind)
  .reduce((a, b) => (a && a.value <= b.value ? a : b), null);
const emptyKinds = () => kinds().filter((k) => !picked.some((v) => comp(v).kind === k));

// The cheapest part in any empty bay: the most likely next click, and the one
// the mass projection is quoted for.
const nextPart = () => emptyKinds().map(cheapestOf)
  .reduce((a, b) => (a && (!b || a.value <= b.value) ? a : b), null);

// What the design does not spend. The budget gauge and every empty bay quote the
// same number, so it is computed once — whole robots off the starting budget,
// and the remainder is not saved. An illegal parts list has no fleet count yet;
// it is still costing one robot's worth of budget.
function unspent() {
  if (!preview) return 0;
  return Math.max(0, preview.budget - Math.max(preview.fleet, 1) * preview.value);
}

const deadTotal = (p) => ((p && p.programs) || []).reduce((n, x) => n + x.dead, 0);

// How many rules the cheapest part in this bay would switch on: the drop in the
// server's own dead-predicate count between this design and that one. Only
// meaningful when both are legal — the answer for an illegal design is not
// "four more rules", it is the §6.3 error the budget gauge is already showing.
function unlocks(kind) {
  const h = hypoFor === key() ? hypo[kind] : null;
  if (!h || !h.ok || !preview || !preview.ok) return 0;
  return Math.max(0, deadTotal(preview) - deadTotal(h));
}

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

// The kind's colour is a mark at the end of the label row, not a bar down the
// side of the card: a full-height stripe reads as a severity, and a component
// kind is not one.
function chip(kind) {
  const c = el("span", { className: "chip", title: kind });
  c.style.background = kindColor(kind);
  return c;
}

function bay(variant, index) {
  const c = comp(variant);
  const drop = el("button", { className: "drop", textContent: "✕", title: `remove ${c.name}` });
  drop.addEventListener("click", () => { picked.splice(index, 1); refresh(); });
  const says = bayLine(c.kind, c.name);
  return el("div", { className: "bay" },
    el("div", { className: "top" },
      el("span", { className: "label", textContent: kindLabel(c.kind) }),
      el("span", { className: "grow" }),
      drop,
      chip(c.kind)),
    el("div", { className: "what" },
      el("b", { textContent: c.name }),
      el("span", { className: "cost", textContent: `${c.value} budget · ${c.mass} mass` })),
    says && el("div", { className: "says", textContent: says }));
}

// The one sentence the server already wrote about this bay. The consequence
// column carries all of them at once; a player reading a bay wants the one that
// is about the part in it, so they are keyed back onto their bay here.
//
// Keyed on the shape of each sentence rather than on its position, because the
// positions move: internal/server/blueprint.go emits one to three speed lines
// depending on the traversal matrix and one armament line per weapon fitted.
// Display only — the strings themselves are never parsed for numbers.
function bayLine(kind, name) {
  const lines = (preview && preview.ok && preview.consequences) || [];
  switch (kind) {
    case "locomotion":
      // The pace on open ground is what the speed gauge says two columns over;
      // where this chassis may and may not go is what only this bay says.
      return lines.find((s) => /^(Cannot enter|Faster on)/.test(s)) ||
             lines.find((s) => s.includes("open ground"));
    case "armor":
      return lines.find((s) => s.includes("health"));
    case "weapon":
      // armamentLines prefixes each weapon's line with the variant's own name.
      return lines.find((s) => s.startsWith(`${name}:`));
    case "radar":
      return lines.find((s) => s.includes("wedge"));
  }
  // The manipulator has no line: design 1f wants "required by N actions in the
  // installed program" there, and no endpoint answers that question yet.
  return null;
}

// An empty bay is drawn rather than omitted: a slot you have not spent is a
// decision the player is making, and design 1f says what makes it one — what
// the budget still has for it, and what installing it would switch on.
function emptyBay(kind) {
  const cheapest = cheapestOf(kind);
  return el("div", { className: "bay empty" },
    el("div", { className: "top" },
      el("span", { className: "label", textContent: `${kindLabel(kind)} · empty` })),
    el("div", { className: "what", textContent: emptyLine(kind, cheapest) }));
}

function emptyLine(kind, cheapest) {
  if (!cheapest) return "nothing in the catalogue fits";
  const left = unspent();
  const fits = cheapest.value <= left
    ? `a ${cheapest.name} fits`
    : `the cheapest ${kindLabel(kind)} is a ${cheapest.name}, at ${cheapest.value}`;
  const n = unlocks(kind);
  return `${left} budget left — ${fits}` +
    (n > 0 ? `, and ${n} rule${n === 1 ? "" : "s"} unlock` : "");
}

function renderPalette() {
  const host = $("palette");
  host.replaceChildren();
  for (const kind of kinds()) {
    host.append(el("span", { className: "label kindname", textContent: kindLabel(kind) }));
    for (const c of lang.components.filter((x) => x.kind === kind)) {
      const b = el("button", { textContent: `+ ${c.name}`, title: `${c.value} budget · ${c.mass} mass` });
      b.addEventListener("click", () => { picked.push(c.variant); refresh(); });
      host.append(b);
    }
  }
}

// ---------------------------------------------------------------------------
// The two gauges
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
      const part = el("span", { className: "part", title: `${c.name} — ${c.value}` });
      part.style.width = `${(c.value / value) * 100}%`;
      part.style.background = kindColor(c.kind);
      unit.append(part);
    }
    track.append(unit);
  }
  const left = unspent();
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
  // The leftover is the part players read as saved. It is not.
  if (left > 0) {
    host.append(el("div", { className: "meta", textContent:
      `${left} unspent. Unspent budget is not saved — it is a robot the base did not build.` }));
  }
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
  const cand = nextPart();
  const h = cand && hypoFor === key() ? hypo[cand.kind] : null;
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
  // Design 1f: what the next part would cost in pace, before it is bought. The
  // server priced it — see reload() — because the §6.4 speed model is not
  // arithmetic this file may repeat.
  if (h && h.ok) {
    host.append(el("div", { className: "meta", textContent:
      h.ticks_per_cell === t
        ? `Adding a ${cand.name} → still ${t} tick${t === 1 ? "" : "s"} a cell.`
        : `Adding a ${cand.name} → ${h.ticks_per_cell} ticks a cell.` }));
  }
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
    // Only the glyph dims on a program that will not run. The name is the thing
    // the player has to go and fix, so greying it out is exactly backwards.
    host.append(el("span", { className: p.ok ? "" : "no", textContent: p.ok ? "✓" : "✗" }));
    const line = el("span", {}, el("span", { textContent: p.name }));
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
  $("sil-desc").replaceChildren();
  if (!preview || !preview.ok) {
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

  // A legend, not a sentence: every row names one mark on the drawing above and
  // says what it means, so a player can look from one to the other. Four prose
  // clauses in a row cannot be looked up that way (design 957-962).
  const facts = [
    ["shape", `chassis is ${loco ? loco.name : "unknown"} — teammates read it at a glance`],
    armed ? ["barrel", "past the nose: every other player can see it is armed"]
          : ["no barrel", "reads as unarmed to every other player"],
    radar > 0 ? ["ring", `radar reach, ${radar} cells in every direction`]
              : ["no ring", "no radar, so nothing outside the wedge exists to it"],
    ["wedge", `sight, 90° × ${sight} cells — unchanged by any part`],
  ];
  const legend = $("sil-desc");
  for (const [k, v] of facts) legend.append(el("span", { textContent: k }), el("span", { textContent: v }));
  host.setAttribute("aria-label", facts.map(([k, v]) => `${k}: ${v}`).join("; ") + ".");
}

// ---------------------------------------------------------------------------
// Preview: one debounced round trip per edit, and every panel redrawn from it
// ---------------------------------------------------------------------------

function refresh() {
  showName();
  renderBays();
  renderBudget();
  renderSpeed();
  renderConsequences();
  renderFit();
  renderSilhouette();
  $("save").disabled = !(preview && preview.ok && previewFor === key());
  $("save").textContent = editing ? "Save changes" : "Save blueprint";
  for (const id of ["edit", "copy", "del"]) $(id).disabled = !selected();
  clearTimeout(timer);
  // Nothing left to ask: the answer already on screen describes this exact
  // parts list. Without this guard every landed preview scheduled the next one
  // and the page polled the endpoint for as long as it was open.
  if (previewFor === key()) return;
  timer = setTimeout(() => { void reload(key()); }, 150);
}

async function reload(k) {
  try {
    const res = await api("POST", "/api/blueprints/preview", { components: picked });
    // Answers can land out of order; one for a design the player has already
    // moved on from is dropped rather than shown.
    if (!res || k !== key()) return;
    preview = res;
    previewFor = k;
    err("");
    refresh();

    // And then, the same question about the design one part further on. Two
    // panels need it — what the next part does to the pace, and how many rules
    // an empty bay would unlock — and both answers are the server's, not this
    // file's. One extra round trip per empty bay, once per settled edit; the
    // guard in refresh() is what keeps that from repeating.
    const empties = emptyKinds();
    const answers = await Promise.all(empties.map((kind) => {
      const c = cheapestOf(kind);
      return c ? api("POST", "/api/blueprints/preview", { components: [...picked, c.variant] }) : null;
    }));
    if (k !== key()) return;
    hypo = {};
    empties.forEach((kind, i) => { if (answers[i]) hypo[kind] = answers[i]; });
    hypoFor = k;
    refresh();
  } catch (e) { err(e.message); }
}

// Which design is on screen, in the bar. The name field is the draft's identity
// and it is editable, so the bar follows it rather than the library row.
function showName() {
  $("bpname").textContent = $("name").value.trim() || "unsaved design";
}

// load opens a saved design in the draft. inPlace is the difference between the
// Edit button and the Copy button, and it is the only thing that decides whether
// Save writes back to that library row or mints a new one.
function load(bp, inPlace) {
  editing = inPlace ? bp.id : 0;
  picked = [...bp.components];
  $("name").value = inPlace ? bp.name : `${bp.name} mk2`;
  preview = null;
  previewFor = null;
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

// Picking a design from the library opens it to look at, which is a copy: the
// safe default is the one where nothing you do can change a row you did not
// mean to touch. Edit is the deliberate one.
$("library").addEventListener("change", () => { const b = selected(); if (b) load(b, false); });
$("edit").addEventListener("click", () => { const b = selected(); if (b) load(b, true); });
$("copy").addEventListener("click", () => { const b = selected(); if (b) load(b, false); });
$("name").addEventListener("input", showName);
$("clear").addEventListener("click", () => {
  editing = 0; picked = []; preview = null; previewFor = null;
  $("name").value = ""; status(""); refresh();
});

// Deleting a design cannot reach a lobby that already approved it: the approval
// stored a frozen snapshot of the parts list, not this id.
$("del").addEventListener("click", async () => {
  err(""); status("");
  const b = selected();
  if (!b || !confirm(`Delete the blueprint "${b.name}"?`)) return;
  try {
    await api("DELETE", `/api/blueprints/${b.id}`);
    blueprints = blueprints.filter((x) => x.id !== b.id);
    if (editing === b.id) editing = 0; // the draft is a new design now, not an edit
    // Emptying the library re-seeds the starter kit on the next read, so the
    // picker is never left with nothing in it.
    if (blueprints.length === 0) blueprints = (await api("GET", "/api/blueprints")).blueprints;
    renderLibrary();
    status(`Deleted. Matches already running on it are unaffected.`);
  } catch (e) { err(e.message); }
});

$("save").addEventListener("click", async () => {
  err(""); status("");
  const body = { name: $("name").value, components: picked };
  try {
    const bp = editing
      ? await api("PUT", `/api/blueprints/${editing}`, body)
      : await api("POST", "/api/blueprints", body);
    if (!bp) return;
    const at = blueprints.findIndex((x) => x.id === bp.id);
    if (at < 0) blueprints.push(bp); else blueprints[at] = bp;
    editing = bp.id;
    renderLibrary();
    $("library").value = String(bp.id);
    $("name").value = bp.name;
    showName();
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
      load(blueprints[0], false);
    } else {
      refresh();
    }
  } catch (e) { err(e.message); }
})();
