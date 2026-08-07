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
import { t } from "./i18n.js";

const $ = (id) => document.getElementById(id);
const SVGNS = "http://www.w3.org/2000/svg";

// Every user-visible sentence is translated whole and its numbers dropped in
// afterwards. web/i18n_test.go only sees a key when the argument is a plain
// literal, so a template literal wrapped round a translation is a string that
// walks past the guard — and a German sentence is not the English one with the
// values in the same places anyway.
//
// The replacement is a function on purpose: a value carrying $& or $1 is read
// as a backreference by the string form, and blueprint names are the player's
// own text.
const fill = (s, ...vals) => vals.reduce((out, v, i) => out.replace(`%${i + 1}`, () => String(v)), s);

// German pluralises the tick too, so the word is looked up rather than built
// out of an English "s".
const tickWord = (n) => (n === 1 ? t("tick") : t("ticks"));

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
// The library row the draft was opened from, whether to edit it or to copy it.
// Not the same question as `editing`: Copy writes to a new row but the approvals
// panel still has to say what the row it came from is approved as.
let origin = 0;
let preview = null;     // the last /api/blueprints/preview answer
let previewFor = null;  // the parts list that answer describes
let lobbies = [];       // GET /api/lobbies, for the approvals panel
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

// ---------------------------------------------------------------------------
// The marginal answer
//
// What a part costs is not what its catalogue row says: the same laser is free
// on a light scavenger and takes a whole robot off the opening fleet on a heavy
// gunner. The server prices every catalogue row against the exact parts list on
// screen and sends the lot on the preview (internal/server/blueprint.go), so
// this file looks the answer up rather than asking again per row.
// ---------------------------------------------------------------------------

// Null until the answer on hand is about the parts list on screen. A marginal
// price is a verdict on a click the player is about to make, so an answer for
// the design they just changed is worse than none: the palette falls back to the
// catalogue price for the round trip rather than promising the wrong fit.
const marginal = (variant) =>
  (previewFor === key() && preview ? preview.marginal || [] : []).find((m) => m.variant === variant) || null;

// One verdict per part, said in one place so the palette and the empty bays
// cannot drift apart. "Fits" is not "cheaper than the leftover" — the leftover
// is not saved (it is base inventory), so what a part really costs is whether
// the starting budget still opens with as many robots once it is fitted.
function verdict(m) {
  if (!m.ok) return m.error;
  if (m.fleet === 0) return t("over budget");
  if (preview && preview.ok && m.fleet < preview.fleet) return fill(t("%1 robots, not %2"), m.fleet, preview.fleet);
  return t("fits");
}

// The whole marginal line: whether it fits, the pace it leaves, and how many
// rows of the rule language it switches on. Every number is the server's.
function priced(m) {
  if (!m.ok) return m.error;
  const tpc = m.ticks_per_cell;
  const pace = preview && tpc === preview.ticks_per_cell ? fill(t("still %1"), tpc) : `→ ${tpc}`;
  const line = `${verdict(m)} · ${pace} ${tickWord(tpc)}/${t("cell")}`;
  const unlocks = m.unlocks === 1 ? t("%1 rule unlock") : t("%1 rules unlock");
  return m.unlocks > 0 ? `${line} · ${fill(unlocks, m.unlocks)}` : line;
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
  const drop = el("button", { className: "drop", textContent: "✕", title: fill(t("remove %1"), c.name) });
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
      el("span", { className: "cost", textContent: fill(t("%1 budget · %2 mass"), c.value, c.mass) })),
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
      el("span", { className: "label", textContent: `${kindLabel(kind)} · ${t("empty")}` })),
    el("div", { className: "what", textContent: emptyLine(kind, cheapest) }));
}

function emptyLine(kind, cheapest) {
  if (!cheapest) return t("nothing in the catalogue fits");
  const left = unspent();
  const m = marginal(cheapest.variant);
  if (!m) {
    return fill(t("%1 budget left — the cheapest %2 is a %3, at %4"),
      left, kindLabel(kind), cheapest.name, cheapest.value);
  }
  return fill(t("%1 budget left — a %2: %3"), left, cheapest.name, priced(m));
}

// The palette prices itself against the design on screen rather than off the
// catalogue: a row says what adding *it* would do to *this* robot. The catalogue
// price stays on the button's title — it is the number a player checks, not the
// one they decide on.
//
// A part §6.3 refuses is dimmed and still clickable: the reason is the answer,
// and taking the click away would leave the player guessing which of the parts
// already fitted is in the way.
function renderPalette() {
  const host = $("palette");
  host.replaceChildren();
  if (!lang) return;
  for (const kind of kinds()) {
    host.append(el("span", { className: "label kindname", textContent: kindLabel(kind) }));
    for (const c of lang.components.filter((x) => x.kind === kind)) {
      const m = marginal(c.variant);
      const price = fill(t("%1 budget · %2 mass"), c.value, c.mass);
      const b = el("button", { className: m && !m.ok ? "no" : "", title: price },
        el("span", { textContent: `+ ${c.name}` }),
        el("span", { className: "say", textContent: m ? priced(m) : price }));
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
      el("span", { textContent: fill(t("%1 each"), value) }),
      el("b", { textContent: `${fleet} × ${value} / ${budget}` })),
    track,
    // What the picture means, not what it costs — the consequence column says
    // that, and saying it twice in different words is how two answers drift.
    el("div", { className: "meta", textContent: preview.ok
      ? t("One block per robot the starting budget opens with, split by the part each point went on.")
      : t("Legal designs only: the budget cannot field something §6.3 refuses to build.") }),
  );
  // The leftover is the part players read as saved. It is not.
  if (left > 0) {
    host.append(el("div", { className: "meta", textContent:
      fill(t("%1 unspent. Unspent budget is not saved — it is a robot the base did not build."), left) }));
  }
}

// Ticks per cell is a count, so it is drawn as that many blocks. A proportional
// bar would need a scale — the speed of the fastest legal robot — and that is a
// balance number this file is not allowed to know.
function renderSpeed() {
  const host = $("speed");
  host.replaceChildren();
  if (!preview) return;
  const tpc = preview.ticks_per_cell;
  const ticks = el("div", { className: "ticks" });
  for (let i = 0; i < Math.min(tpc, 16); i++) ticks.append(el("i"));
  const cand = nextPart();
  const h = cand && marginal(cand.variant);
  host.append(
    el("div", { className: "head" },
      el("span", { textContent: fill(t("%1 mass"), preview.mass) }),
      el("b", { textContent: `1 ${t("cell")} / ${tpc} ${tickWord(tpc)}` })),
    ticks,
    // One block is one tick. No proportional bar and no seconds: the arena size
    // and the tick rate are the match's, not this page's, and a sentence that
    // assumed either would be wrong on the first lobby that changed one.
    el("div", { className: "meta", textContent:
      fill(t("Speed %1 on open ground. Every part you add is mass, and mass is blocks."), preview.speed) }),
  );
  // Design 1f: what the next part would cost in pace, before it is bought. It
  // rides the preview with every other row's price, because the §6.4 speed model
  // is not arithmetic this file may repeat.
  if (h && h.ok) {
    const n = h.ticks_per_cell;
    host.append(el("div", { className: "meta", textContent:
      fill(t("Adding a %1 → %2 %3 a cell."), cand.name, n === tpc ? fill(t("still %1"), n) : n, tickWord(n)) }));
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
      el("span", { className: "meta", textContent: t("Finish the design and every program in your library is checked against it.") }));
    return;
  }
  const list = preview.programs || [];
  if (list.length === 0) {
    host.append(el("span", { className: "meta", textContent: "—" }),
      el("span", { className: "meta" }, el("a", { href: "/editor", textContent: t("Your library has no programs yet.") })));
    return;
  }
  for (const p of list) {
    // Only the glyph dims on a program that will not run. The name is the thing
    // the player has to go and fix, so greying it out is exactly backwards.
    host.append(el("span", { className: p.ok ? "" : "no", textContent: p.ok ? "✓" : "✗" }));
    const line = el("span", {}, el("span", { textContent: p.name }));
    if (!p.ok) line.append(el("span", { className: "why", textContent: ` — ${p.blocked}` }));
    else if (p.dead > 0) {
      const dead = p.dead === 1
        ? t("%1 dead rule: hardware this design does not carry")
        : t("%1 dead rules: hardware this design does not carry");
      line.append(el("span", { className: "why", textContent: ` — ${fill(dead, p.dead)}` }));
    }
    host.append(line);
  }
}

// ---------------------------------------------------------------------------
// What your base will build
//
// Approval is a lobby-only act: it lives on the lobby_members row as a frozen
// snapshot of the parts list, not as a property of the library
// (internal/lobby/loadout.go). There is no "approved" state a design has outside
// a lobby, so this panel is read-only and does no more than report the seats the
// player already holds.
//
// It matches on the parts list rather than on the library id, because that is
// what the base builds: approving freezes a copy, so a design edited since is
// approved under its old parts list and the honest answer is to say so.
//
// It implies no order on purpose. startingRoster draws each robot uniformly
// from every approval that still fits the budget (loadout.go), and a numbered
// list here would be a promise the simulation does not make.
// ---------------------------------------------------------------------------

// Two parts lists are the same design to the base if they hold the same parts;
// slot order is not something the simulation reads.
const partsKey = (list) => [...list].sort((a, b) => a - b).join(",");

// My seat is the only one carrying a loadout — LobbyView.forUser clears every
// other member's, so nothing here has to know who I am.
const mySeat = (l) => (l.members || []).find((m) => m.loadout);

function renderApprovals() {
  const host = $("approvals");
  host.replaceChildren();
  if (picked.length === 0) {
    host.append(el("p", { className: "meta", textContent: t("An empty parts list is not a design yet.") }));
    return;
  }
  const mine = partsKey(picked);
  const builds = [], stale = [];
  for (const l of lobbies) {
    const seat = mySeat(l);
    const entries = (seat && seat.loadout.entries) || [];
    if (entries.some((e) => partsKey(e.components) === mine)) builds.push(l);
    else if (origin && entries.some((e) => e.blueprint_id === origin)) stale.push(l);
  }
  const links = (list) => list.flatMap((l, i) =>
    [i ? el("span", { textContent: ", " }) : null, el("a", { href: "/lobby", textContent: l.name })]);

  // The prose around each link is marked one fragment at a time: German puts
  // the clauses in a different order, and a sentence assembled out of a link
  // and two halves can only be reordered where the halves are whole clauses.
  if (builds.length > 0) {
    const seats = builds.length === 1
      ? t("Approved in %1 open lobby of yours:")
      : t("Approved in %1 open lobbies of yours:");
    host.append(el("p", {},
      el("span", { textContent: `${fill(seats, builds.length)} ` }),
      ...links(builds)));
    host.append(el("p", { className: "meta", textContent:
      t("Every robot the starting budget opens with is drawn from your approvals at random, so an approved design has no place in a queue — only a share of the draw.") }));
  } else {
    host.append(el("p", { className: "meta" },
      el("span", { textContent: `${t("No open lobby of yours approves this parts list. Approval is per lobby, not a property of the design — approve it on the")} ` }),
      el("a", { href: "/lobby", textContent: t("lobbies page") }),
      el("span", { textContent: ` ${t("and your base there will build it.")}` })));
  }
  if (stale.length > 0) {
    host.append(el("p", { className: "meta" },
      el("span", { textContent: `${t("Approved under an earlier parts list in")} ` }),
      ...links(stale),
      el("span", { textContent: `: ${t("approving freezes a copy, so the base there still builds that one until you approve this design again.")}` })));
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
    host.setAttribute("aria-label", t("no silhouette: the design is not legal yet"));
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
  // The chassis is named, never translated: it is the component's own name, the
  // one the catalogue and the wire use. Everything said *about* it is prose.
  const facts = [
    [t("shape"), fill(t("chassis is %1 — teammates read it at a glance"), loco ? loco.name : t("unknown"))],
    armed ? [t("barrel"), t("past the nose: every other player can see it is armed")]
          : [t("no barrel"), t("reads as unarmed to every other player")],
    radar > 0 ? [t("ring"), fill(t("radar reach, %1 cells in every direction"), radar)]
              : [t("no ring"), t("no radar, so nothing outside the wedge exists to it")],
    [t("wedge"), fill(t("sight, 90° × %1 cells — unchanged by any part"), sight)],
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
  renderPalette();
  renderApprovals();
  renderBudget();
  renderSpeed();
  renderConsequences();
  renderFit();
  renderSilhouette();
  $("save").disabled = !(preview && preview.ok && previewFor === key());
  $("save").textContent = editing ? t("Save changes") : t("Save blueprint");
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
  } catch (e) { err(e.message); }
}

// Which design is on screen, in the bar. The name field is the draft's identity
// and it is editable, so the bar follows it rather than the library row.
function showName() {
  $("bpname").textContent = $("name").value.trim() || t("unsaved design");
}

// load opens a saved design in the draft. inPlace is the difference between the
// Edit button and the Copy button, and it is the only thing that decides whether
// Save writes back to that library row or mints a new one.
function load(bp, inPlace) {
  editing = inPlace ? bp.id : 0;
  origin = bp.id;
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
  sel.replaceChildren(el("option", { value: "", textContent: fill(t("%1 saved…"), blueprints.length) }));
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
  editing = 0; origin = 0; picked = []; preview = null; previewFor = null;
  $("name").value = ""; status(""); refresh();
});

// Deleting a design cannot reach a lobby that already approved it: the approval
// stored a frozen snapshot of the parts list, not this id.
$("del").addEventListener("click", async () => {
  err(""); status("");
  const b = selected();
  // The quotes live outside the key: web/i18n_test.go will not read a literal
  // that carries one, and it is right not to — the escaping is not translatable.
  if (!b || !confirm(fill(t("Delete the blueprint %1?"), `"${b.name}"`))) return;
  try {
    await api("DELETE", `/api/blueprints/${b.id}`);
    blueprints = blueprints.filter((x) => x.id !== b.id);
    if (editing === b.id) editing = 0; // the draft is a new design now, not an edit
    // Emptying the library re-seeds the starter kit on the next read, so the
    // picker is never left with nothing in it.
    if (blueprints.length === 0) blueprints = (await api("GET", "/api/blueprints")).blueprints;
    renderLibrary();
    status(t("Deleted. Matches already running on it are unaffected."));
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
    editing = origin = bp.id;
    renderLibrary();
    $("library").value = String(bp.id);
    $("name").value = bp.name;
    showName();
    status(t("Saved. Approve it on the lobbies page and your base will build it."));
  } catch (e) { err(e.message); }
});

(async function start() {
  try {
    lang = await api("GET", "/api/language");
    if (!lang) return;
    $("name").maxLength = lang.limits.max_name_len;
    blueprints = (await api("GET", "/api/blueprints")).blueprints;
    // Which of the caller's lobbies approve the design on screen. Read once:
    // nothing on this page can change an approval, and a lobby list that failed
    // to load costs the page one panel rather than the configurator.
    lobbies = ((await api("GET", "/api/lobbies").catch(() => null)) || {}).lobbies || [];
    renderLibrary();
    if (blueprints.length > 0) {
      $("library").value = String(blueprints[0].id);
      load(blueprints[0], false);
    } else {
      refresh();
    }
  } catch (e) { err(e.message); }
})();
