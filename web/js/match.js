// Live match observer: canvas arena, robot inspector, colony stats.
//
// Renders strictly from the last snapshot the server sent. There is no
// interpolation and no client-side simulation: the server is authoritative, and
// a guess drawn between ticks would visibly disagree with the next frame.

const $ = (id) => document.getElementById(id);
const err = (m) => { $("err").textContent = m || ""; };

const matchID = new URLSearchParams(location.search).get("id");

// Heading is 0..7 clockwise from north (sim.Heading).
const HEADINGS = ["N", "NE", "E", "SE", "S", "SW", "W", "NW"];
const DELTA = [[0, -1], [1, -1], [1, 0], [1, 1], [0, 1], [-1, 1], [-1, 0], [-1, -1]];

// Forward vision, design §7.1 / sim.inCone: range 6 in Chebyshev distance and a
// 90° wedge (cos² of the half-angle is 1/2). Duplicated here on purpose — the
// wire carries state, not rules, and drawing the wrong cone is worse than
// drawing none.
const VISION_RANGE = 6;

const css = (name, fallback) => {
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return v || fallback;
};
const colonyVar = (id) => `--colony-${((id % 8) + 8) % 8}`;
const colonyColor = (id) => css(colonyVar(id), "#888");
const slug = (s) => String(s).toLowerCase().replace(/[^a-z0-9]+/g, "-");

const SVGNS = "http://www.w3.org/2000/svg";

let init = null;       // init frame
let snap = null;       // last tick frame
let over = false;      // end frame seen
let selected = null;   // robot id, persists across frames
let terrain = null;    // offscreen canvas, drawn once
let cell = 12;
const buildTotals = new Map(); // colony|blueprint -> longest build seen, for the progress bar

const canvas = $("arena");
const ctx = canvas.getContext("2d");

// ---------------------------------------------------------------- terrain

// Terrain is painted once into an offscreen canvas and blitted every frame.
// Repainting 4096 cells ten times a second is the obvious performance cliff.
function bakeTerrain() {
  cell = Math.max(6, Math.min(24, Math.floor(900 / Math.max(init.width, init.height))));
  canvas.width = init.width * cell;
  canvas.height = init.height * cell;
  canvas.style.maxWidth = `${canvas.width}px`; // never upscale past the baked size

  const colors = init.terrain_legend.map((t) => css(`--terrain-${slug(t.name)}`, css("--terrain-unknown", "#888")));
  terrain = document.createElement("canvas");
  terrain.width = canvas.width;
  terrain.height = canvas.height;
  const t = terrain.getContext("2d");
  for (let y = 0; y < init.height; y++) {
    const row = init.terrain[y] || "";
    for (let x = 0; x < init.width; x++) {
      t.fillStyle = colors[row.charCodeAt(x) - 48] || colors[0];
      t.fillRect(x * cell, y * cell, cell, cell);
    }
  }
  t.strokeStyle = css("--grid-line", "#0001");
  t.lineWidth = 1;
  for (let i = 0; i <= init.width; i++) {
    t.beginPath(); t.moveTo(i * cell + .5, 0); t.lineTo(i * cell + .5, terrain.height); t.stroke();
  }
  for (let i = 0; i <= init.height; i++) {
    t.beginPath(); t.moveTo(0, i * cell + .5); t.lineTo(terrain.width, i * cell + .5); t.stroke();
  }
}

// ------------------------------------------------------------ silhouettes
//
// A robot's shape is its loadout. Design §1.1's fifth pillar is a *readable*
// simulation, and until this existed every robot on the board was the same
// colony-coloured circle with a letter in it: the only way to tell a tracked
// scavenger from a legged gunner was to click one and read the inspector.
//
// Two facts, and deliberately only two:
//
//   - **Chassis**, because it is the one component every blueprint must carry,
//     and because design §3.1 makes it the robot's identity: where it can go,
//     what slows it down, what it can cross. Angular, limbed and hovering read
//     apart at a glance.
//   - **Armed or not**, because that is the question a player watching a fight
//     is actually asking.
//
// Armour tier, weapon *count*, radar and manipulator are not encoded. A cell is
// about 14px at the default arena size; an outline weight, a second barrel or a
// dish are invisible there, and a silhouette that tries to say six things says
// none. The manipulator is already implied by the cargo dot, and the radars
// change what a robot knows rather than what it does.
//
// The paths are unit space, centred on the origin, spanning about ±1, and drawn
// pointing north — heading 0 — so one rotate() orients a robot and the shape's
// nose *is* the heading mark. That replaces the black notch line: forward vision
// is the core mechanic, and a shape whose front you can see says it without
// spending a second mark on it.
//
// The same strings drive the canvas (through Path2D) and the legend's inline
// SVG, which is the only reason the legend cannot drift from the map.
const SHAPES = {
  // Tracks: flat sides, square tail, blunt mass. The workhorse reads as a hull.
  tracks: "M0-1.25L.9-.5L.9.9L-.9.9L-.9-.5Z",
  // Legs: the same body carried on splayed limbs. The spurs are the silhouette.
  legs: "M0-1.15L.5-.55L1.15-.45L.6.05L1 1.15L.3.45L-.3.45L-1 1.15L-.6.05L-1.15-.45L-.5-.55Z",
  // Anti-gravity: a smooth lens with nothing angular on it and no ground contact.
  "anti-gravity-platform": "M0-1.25C.85-.8.95.3 0 .95C-.95.3-.85-.8 0-1.25Z",
  // A locomotion the catalogue has grown and this file has never heard of, or a
  // robot whose blueprint is not on this frame: a plain body, still with a nose,
  // so an unknown chassis never costs the player the heading.
  unknown: "M0-1.3L.72-.7A1 1 0 1 1-.72-.7Z",
};

// A barrel past the nose, in the same unit space and in the weapon colour the
// loose components and the legend already use. Binary: one weapon or two draws
// the same barrel, because two barrels 2px apart is noise, not information.
const MUZZLE = "M-.26-.9L.26-.9L.26-1.8L-.26-1.8Z";

const BODY = Object.fromEntries(Object.entries(SHAPES).map(([k, d]) => [k, new Path2D(d)]));
const BARREL = new Path2D(MUZZLE);

// Silhouettes are cached per (colony, blueprint): up to ~160 robots are redrawn
// ten times a second and none of them may walk the base's blueprint list to do
// it. A blueprint id means one design for the whole match, so the entry never
// goes stale; the map is cleared with the rest of the match state on init.
const styles = new Map();
const UNKNOWN_STYLE = { shape: "unknown", armed: false };

function robotStyle(r) {
  const key = `${r.colony}|${r.blueprint}`;
  const hit = styles.get(key);
  if (hit) return hit;
  const bp = blueprintOf(r);
  // Not cached: an id the init frame's design list does not name is a client
  // older than the world it is watching, not a design that will appear later.
  if (!bp) return UNKNOWN_STYLE;
  const parts = bp.components.map(catalogue);
  const loco = parts.find((c) => c && c.kind === "locomotion");
  const name = loco ? slug(loco.name) : "unknown";
  const style = {
    shape: SHAPES[name] ? name : "unknown",
    armed: parts.some((c) => c && c.kind === "weapon"),
  };
  styles.set(key, style);
  return style;
}

// ---------------------------------------------------------------- legend
//
// Built once from the init frame, into its own container under the canvas —
// never inside #inspector, which is cleared with replaceChildren() every tick.
//
// A swatch carries `var(--…)`, not a resolved colour: it is then literally the
// same custom property bakeTerrain and drawLoose read, so the legend cannot
// disagree with the map and it follows the theme with no re-render. Duplicating
// a colour as a literal here is exactly how a legend goes quietly wrong.
//
// Terrain and component kinds are built from the wire, not from a list of four
// and a list of five: a terrain class or a catalogue kind added server-side
// appears in the legend with no client change.
//
// The same goes for what a terrain class *does*: the §3.1 traversal matrix is
// read off init.terrain_legend, never restated here. A locomotion is named by
// resolving its variant id against the component catalogue, so the legend and
// the simulation cannot drift apart — not even by a rename.

function legendList(host, items) {
  host.replaceChildren(...items.map(([label, color]) => {
    const li = el("li");
    const sw = el("span", "swatch");
    sw.style.background = color;
    li.append(sw, document.createTextNode(label));
    return li;
  }));
}

// bodyPath is one silhouette as SVG. Both the chassis list and the static mark
// glyphs in match.html go through it, so every robot drawn in the legend is
// literally the path the canvas fills. Stroke and fill come from CSS, and
// vector-effect keeps the outline one weight whatever the <g> is scaled to.
function bodyPath(d, cls) {
  const p = document.createElementNS(SVGNS, "path");
  p.setAttribute("d", d);
  p.setAttribute("class", cls || "body");
  return p;
}

function shapeGlyph(shape, armed) {
  const svg = document.createElementNS(SVGNS, "svg");
  svg.setAttribute("viewBox", "0 0 16 16");
  svg.setAttribute("aria-hidden", "true");
  const g = document.createElementNS(SVGNS, "g");
  // An armed glyph has to fit a barrel 1.8 units out; an unarmed one does not.
  g.setAttribute("transform", `translate(8 8) scale(${armed ? 4.2 : 5.6})`);
  if (armed) g.append(bodyPath(MUZZLE, "body barrel"));
  g.append(bodyPath(SHAPES[shape] || SHAPES.unknown));
  svg.append(g);
  return svg;
}

function legendShapes(host, items) {
  host.replaceChildren(...items.map(([label, shape, armed, note]) => {
    const li = el("li");
    li.append(shapeGlyph(shape, armed), document.createTextNode(label));
    if (note) li.append(el("span", "note", ` — ${note}`));
    return li;
  }));
}

function terrainLabel(t) {
  const names = (vs) => vs.map(compName).join(", ");
  const effects = [];
  if (t.hard_barrier) effects.push("blocks everything");
  else if (t.impassable?.length) effects.push(`blocks ${names(t.impassable)}`);
  if (t.favored?.length) effects.push(`favours ${names(t.favored)}`);
  return effects.length ? `${t.name} — ${effects.join(", ")}` : t.name;
}

function buildLegend() {
  legendList($("lg-terrain"), init.terrain_legend.map((t) =>
    [terrainLabel(t), `var(--terrain-${slug(t.name)}, var(--terrain-unknown))`]));

  const kinds = [...new Set(init.components.map((c) => c.kind))].sort();
  legendList($("lg-kinds"), kinds.map((k) =>
    [k, `var(--kind-${slug(k)}, var(--kind-unknown))`]));

  legendList($("lg-colonies"), init.colonies.map((c) =>
    [c.display_name, `var(${colonyVar(c.id)})`]));

  // Chassis, off the catalogue rather than off a list of three: a locomotion
  // added server-side appears here either with its own silhouette or visibly
  // falling back to the plain body, which is the signal to draw it one.
  const locos = init.components.filter((c) => c.kind === "locomotion");
  legendShapes($("lg-chassis"), locos.map((c) => [c.name, slug(c.name), false, ""]).concat([
    ["armed", locos.length ? slug(locos[0].name) : "unknown", true,
      "a weapon of any kind, on any chassis, puts a barrel past the nose"],
  ]));

  // The mark glyphs draw a real body rather than a stand-in circle, from the
  // same table: a mark that illustrated a robot the renderer never draws is
  // exactly how a legend goes quietly wrong. Tracks is the exemplar; the chassis
  // list above is where the shapes are told apart.
  for (const g of document.querySelectorAll(".lg-body")) {
    g.replaceChildren(bodyPath(SHAPES.tracks));
  }
}

// ---------------------------------------------------------------- drawing

// weaponColor is read once per frame rather than once per armed robot: css()
// is a getComputedStyle call, and there are up to ~160 robots ten times a
// second. Per frame it still follows a theme switch without a reload.
let weaponColor = "#c23b3b";

function draw() {
  if (!init || !terrain) return;
  ctx.drawImage(terrain, 0, 0);
  if (!snap) return;
  weaponColor = css("--kind-weapon", "#c23b3b");

  const sel = snap.robots.find((r) => r.id === selected);
  if (sel) drawVision(sel);

  for (const b of snap.bases) drawBase(b);
  for (const l of snap.loose) drawLoose(l);
  for (const r of snap.robots) drawRobot(r, r.id === selected);
}

// drawVision paints the exact cells sim.inCone reports, not an approximate arc:
// the wedge is Chebyshev-ranged, so an arc would lie at the corners.
function drawVision(r) {
  const [hx, hy] = DELTA[r.heading % 8];
  const hsq = hx * hx + hy * hy;
  ctx.fillStyle = colonyColor(r.colony);
  ctx.globalAlpha = .18;
  for (let dy = -VISION_RANGE; dy <= VISION_RANGE; dy++) {
    for (let dx = -VISION_RANGE; dx <= VISION_RANGE; dx++) {
      const dot = dx * hx + dy * hy;
      if (dot <= 0) continue;
      const dsq = dx * dx + dy * dy;
      if (dot * dot * 2 < dsq * hsq) continue;
      ctx.fillRect((r.x + dx) * cell, (r.y + dy) * cell, cell, cell);
    }
  }
  ctx.globalAlpha = 1;
}

function drawBase(b) {
  const c = colonyColor(b.colony);
  const p = cell * 0.15;
  ctx.fillStyle = c;
  ctx.fillRect(b.x * cell - p, b.y * cell - p, cell + 2 * p, cell + 2 * p);
  ctx.strokeStyle = "#000";
  ctx.lineWidth = 1;
  ctx.strokeRect(b.x * cell - p, b.y * cell - p, cell + 2 * p, cell + 2 * p);
  glyph("B", b.x, b.y, cell * 0.8);
}

function drawLoose(l) {
  const kind = catalogue(l.variant)?.kind || "unknown";
  ctx.fillStyle = css(`--kind-${slug(kind)}`, css("--kind-unknown", "#999"));
  const cx = l.x * cell + cell / 2, cy = l.y * cell + cell / 2, r = cell * 0.28;
  ctx.beginPath();
  ctx.moveTo(cx, cy - r); ctx.lineTo(cx + r, cy); ctx.lineTo(cx, cy + r); ctx.lineTo(cx - r, cy);
  ctx.closePath();
  ctx.fill();
}

function drawRobot(r, isSelected) {
  // rad and the glyph size are the two numbers that decide whether any of this
  // is readable at ~14px a cell. The body is a little larger than the circle it
  // replaces and the letter a little smaller, because a letter sized to fill a
  // circle covers the outline that now carries the loadout. Neither changes what
  // a click selects: the hit test is in cells (see HIT_CELLS).
  const cx = r.x * cell + cell / 2, cy = r.y * cell + cell / 2, rad = cell * 0.44;
  const { shape, armed } = robotStyle(r);

  // The silhouette carries the chassis, the barrel says it is armed, and the
  // nose is the heading — see SHAPES. Drawn in unit space and scaled, so the
  // one place the geometry lives is that table. The black outline is not
  // decoration: it is what separates a robot from the terrain colour under it.
  ctx.save();
  ctx.translate(cx, cy);
  ctx.rotate((r.heading % 8) * Math.PI / 4); // heading 0 is north; the shapes point north
  ctx.scale(rad, rad);
  ctx.lineWidth = 1 / rad; // one pixel, undoing the scale above
  ctx.strokeStyle = "#000";
  if (armed) {
    ctx.fillStyle = weaponColor;
    ctx.fill(BARREL);
    ctx.stroke(BARREL);
  }
  ctx.fillStyle = colonyColor(r.colony);
  ctx.fill(BODY[shape]);
  ctx.stroke(BODY[shape]);
  ctx.restore();

  // Archetype glyph: first letter of the blueprint name, which is player-chosen
  // and therefore cannot come from a hardcoded table.
  glyph((r.archetype || "?").charAt(0).toUpperCase(), r.x, r.y, cell * 0.55);

  if (r.cargo) { // a carried component rides on the shoulder
    ctx.fillStyle = css(`--kind-${slug(catalogue(r.cargo)?.kind || "unknown")}`, "#999");
    ctx.beginPath();
    ctx.arc(cx + rad * 0.8, cy - rad * 0.8, cell * 0.18, 0, Math.PI * 2);
    ctx.fill();
  }
  if (r.hp < r.hp_max) {
    ctx.fillStyle = "#0006";
    ctx.fillRect(r.x * cell, r.y * cell + cell - 2, cell, 2);
    ctx.fillStyle = r.hp * 2 < r.hp_max ? "#c0392b" : "#c98a00";
    ctx.fillRect(r.x * cell, r.y * cell + cell - 2, cell * (r.hp / r.hp_max), 2);
  }
  if (isSelected) {
    ctx.strokeStyle = "#fff";
    ctx.lineWidth = 2;
    ctx.beginPath();
    ctx.arc(cx, cy, rad + 3, 0, Math.PI * 2);
    ctx.stroke();
    ctx.strokeStyle = "#000";
    ctx.lineWidth = 1;
    ctx.stroke();
  }
}

function glyph(ch, x, y, size) {
  ctx.fillStyle = "#fff";
  ctx.strokeStyle = "#0008";
  ctx.lineWidth = 2;
  ctx.font = `bold ${Math.round(size)}px system-ui, sans-serif`;
  ctx.textAlign = "center";
  ctx.textBaseline = "middle";
  const cx = x * cell + cell / 2, cy = y * cell + cell / 2;
  ctx.strokeText(ch, cx, cy);
  ctx.fillText(ch, cx, cy);
}

const catalogue = (variant) => init?.components.find((c) => c.variant === variant);
const compName = (variant) => catalogue(variant)?.name || `variant ${variant}`;
const colonyName = (id) => init?.colonies.find((c) => c.id === id)?.display_name || `colony ${id}`;

// ---------------------------------------------------------------- selection

// A robot is a dot a few pixels wide that moves every tick or two, so an exact
// cell hit is luck rather than aim. Take the nearest one within a couple of
// cells of the pointer; a click into open ground still deselects.
const HIT_CELLS = 2;

canvas.addEventListener("click", (ev) => {
  if (!init || !snap) return;
  const box = canvas.getBoundingClientRect();
  const px = (ev.clientX - box.left) / box.width * init.width;
  const py = (ev.clientY - box.top) / box.height * init.height;
  let hit = null, best = (HIT_CELLS + .5) ** 2;
  for (const r of snap.robots) {
    const d = (r.x + .5 - px) ** 2 + (r.y + .5 - py) ** 2;
    if (d < best) { best = d; hit = r; }
  }
  selected = hit ? hit.id : null;
  render();
});

// ---------------------------------------------------------------- panels

function el(tag, className, text) {
  const e = document.createElement(tag);
  if (className) e.className = className;
  if (text !== undefined) e.textContent = text;
  return e;
}

function defs(pairs) {
  const dl = el("dl");
  for (const [k, v] of pairs) { dl.append(el("dt", null, k), el("dd", null, v)); }
  return dl;
}

function renderInspector() {
  const box = $("inspector");
  box.replaceChildren();
  if (selected === null) {
    box.append(el("p", "meta", "No robot selected."));
    renderCommand(null);
    return;
  }
  const r = snap?.robots.find((x) => x.id === selected);
  if (!r) {
    box.append(el("p", "meta", `Robot #${selected} is gone — destroyed, or salvaged.`));
    renderCommand(null);
    return;
  }

  const head = el("div");
  const sw = el("span", "swatch");
  sw.style.background = colonyColor(r.colony);
  head.append(sw, el("strong", null, `${r.archetype} #${r.id}`),
    el("span", "meta", ` · ${colonyName(r.colony)}`));
  box.append(head);

  const frac = r.hp_max > 0 ? r.hp / r.hp_max : 0;
  const bar = el("div", "hp" + (frac <= .33 ? " crit" : frac < 1 ? " hurt" : ""));
  const fill = el("i");
  fill.style.width = `${Math.max(0, Math.min(1, frac)) * 100}%`;
  bar.append(fill);
  box.append(bar, el("div", "meta", `${r.hp} / ${r.hp_max} hp`));

  box.append(el("h3", null, "Active rule"));
  box.append(ruleBox(r));

  const bp = blueprintOf(r);
  box.append(el("h3", null, "Loadout"));
  box.append(defs([
    ["Blueprint", bp ? `${bp.name} (${bp.id})` : r.blueprint],
    ["Components", bp ? bp.components.map(compName).join(", ") : "—"],
    ["Program", r.program || "none"],
    ["Cargo", r.cargo ? compName(r.cargo) : "empty"],
    ["Position", `${r.x}, ${r.y} facing ${HEADINGS[r.heading % 8]}`],
    ["Cooldown", r.cooldown > 0 ? `${r.cooldown} ticks` : "ready"],
  ]));

  box.append(el("h3", null, "Memory"));
  // Highlighted straight after an install: design §4.2 step 4 clears all three,
  // and §13 criterion 6 is checked by looking at exactly this list.
  const fresh = note && note.robot === r.id && !note.bad;
  const mem = el("ul", "tight" + (fresh ? " cleared" : ""));
  (r.memory || []).forEach((p, i) => {
    mem.append(el("li", null, `${i + 1}: ` + (p ? `${p.x}, ${p.y}` : "unset")));
  });
  box.append(mem);

  renderCommand(r);
}

// renderCommand keeps the command controls in their own panel, never inside
// #inspector. renderInspector clears that panel with replaceChildren() on every
// tick, which detaches whatever is in it — and a detached <select> loses an open
// dropdown, so the program picker closed itself ~100ms after the player opened
// it. Building the controls once was not enough; the node has to stay attached.
function renderCommand(r) {
  const host = $("command");
  if (!r || r.colony !== myColony()) {
    host.hidden = true;
    if (cmdFor !== null) { host.replaceChildren(); cmd = null; cmdFor = null; }
    return;
  }
  if (cmdFor !== r.id) { // a different robot: new controls, nothing to preserve
    note = null;
    cmd = commandBox(r);
    cmdFor = r.id;
    host.replaceChildren(el("h3", null, "Command"), cmd.node);
    loadPrograms();
  }
  host.hidden = false;
  cmd.update(r); // in place: never detaches cmd.node
}

// ruleName is what took the tick. Rule -1 with an action is not "no rule
// matched" — it is the deposit reflex (design §10.5's caveat), and labelling it
// as a failure to match would make the one automatic action in the game look
// like a bug. The reason line beside it says which reflex.
function ruleName(t) {
  if (t.rule >= 0) return `rule ${t.rule + 1}`;
  return t.action ? "reflex, no rule needed" : "no rule matched";
}

function ruleBox(r) {
  const t = r.trace;
  const div = el("div", "rule");
  div.style.borderLeftColor = colonyColor(r.colony);
  if (!t) {
    div.classList.add("idle");
    div.append(el("div", "idx", "no decision yet"),
      el("div", "why", "the robot has no program, or was built this tick"));
    return div;
  }
  if (t.idle) div.classList.add("idle");
  div.append(el("div", "idx", ruleName(t)));
  div.append(el("div", "act", t.action ? t.action : "idle"));
  div.append(el("div", "why", t.reason || ""));
  if (snap && t.tick !== snap.tick) {
    div.append(el("div", "meta", `decided on tick ${t.tick}, ${snap.tick - t.tick} ticks ago`));
  }
  return div;
}

// Approved designs come from the init frame: they are fixed for the whole match
// and resending them ten times a second was half of an ordinary tick frame
// (rc-w9s.31).
const blueprintsOf = (colony) =>
  init?.colonies.find((c) => c.id === colony)?.blueprints || [];

function blueprintOf(r) {
  return blueprintsOf(r.colony).find((x) => x.id === r.blueprint) || null;
}

// ---------------------------------------------------------------- roster
//
// The roster is the same selection as a canvas click, made by name instead of
// by aim, and it doubles as a colony-wide view of what every robot is doing.
//
// Rows are built once per robot and updated in place, for the same reason
// commandBox is: this panel re-renders ten times a second, and a list rebuilt
// that often loses its scroll position, its hover and the click that is the
// whole point of it. Rows are keyed by robot id — production appends robots and
// destruction removes them, so a slice position is not an identity — and
// ordered by (colony, id), both of which are fixed for a robot's whole life. A
// row therefore never moves out from under the pointer.

const rosterRows = new Map(); // robot id -> {node, update}
let rosterShown = null;       // selection the list last scrolled to

const setText = (n, s) => { if (n.textContent !== s) n.textContent = s; };

function rosterRow(r) {
  const node = el("button", "row");
  node.type = "button";
  const sw = el("span", "swatch");
  sw.style.background = colonyColor(r.colony);
  // Identity is fixed at build time; only the numbers below change per tick.
  const who = el("span", "who", `${r.archetype} #${r.id}`);
  const hp = el("span");
  const act = el("span", "act");
  const cargo = el("span", "cargo");
  const sub = el("span", "sub");
  sub.append(act, cargo);
  node.append(sw, who, hp, sub);
  node.addEventListener("click", () => { selected = r.id; render(); });

  const update = (cur) => {
    setText(hp, `${cur.hp}/${cur.hp_max}`);
    hp.className = cur.hp * 3 <= cur.hp_max ? "num crit" : cur.hp < cur.hp_max ? "num hurt" : "num";

    // A recalled robot has suspended its program (design §4.2), so its trace is
    // stale — say what it is actually doing, not what it last decided.
    const t = cur.trace;
    setText(act, cur.recalled ? "returning to base" : t ? (t.action || "idle") : "no decision yet");
    const why = cur.recalled ? "recalled" : (t && t.reason) || "";
    if (node.title !== why) node.title = why;

    setText(cargo, cur.cargo ? compName(cur.cargo) : "");

    const on = cur.id === selected;
    if (node.classList.contains("sel") !== on) {
      node.classList.toggle("sel", on);
      node.setAttribute("aria-pressed", on ? "true" : "false");
    }
  };

  return { node, update };
}

function renderRoster() {
  const list = $("roster");
  const live = snap ? [...snap.robots].sort((a, b) => a.colony - b.colony || a.id - b.id) : [];
  $("roster-empty").hidden = live.length > 0;

  // Walk the list in order, moving a node only when it is not already where it
  // belongs: in the steady state this touches no DOM at all.
  let at = list.firstChild;
  for (const r of live) {
    let row = rosterRows.get(r.id);
    if (!row) { row = rosterRow(r); rosterRows.set(r.id, row); }
    if (at === row.node) at = at.nextSibling;
    else list.insertBefore(row.node, at);
    row.update(r);
  }

  const ids = new Set(live.map((r) => r.id));
  for (const [id, row] of rosterRows) {
    if (!ids.has(id)) { row.node.remove(); rosterRows.delete(id); }
  }

  // Bring a robot picked on the canvas into view, once per selection: doing it
  // every frame would fight the scrollbar.
  if (selected !== rosterShown) {
    rosterShown = selected;
    rosterRows.get(selected)?.node.scrollIntoView({ block: "nearest" });
  }
}

// ---------------------------------------------------------------- history
//
// Design §10.10's trace history: every rule that matched, which one took the
// tick, the action and its target, memory changes and signals received — for
// ticks that have already gone by.
//
// It is polled from its own endpoint rather than carried on the tick frame, and
// asking for it is what makes the server record it. The frame stays the size it
// was, and the ~160 robots nobody has selected are not recorded at all; see
// internal/server/trace.go.
//
// The list is append-only and never rebuilt. New decisions prepend; a run of
// identical consecutive decisions extends the top entry's tick range in place
// instead of adding a row, which is what turns a 10Hz firehose into something
// readable. Entries carrying a memory change or a signal never collapse — those
// are the ones a player is looking for.

const HISTORY_POLL_MS = 500;
const HISTORY_ROWS = 60;

let histRobot = undefined; // robot the list belongs to; undefined = never set
let histSince = 0;         // newest tick already displayed
let histRun = null;        // {key, row} of the top entry, for run collapsing
let histRows = 0;
let histBusy = false;      // one poll in flight; see pollHistory

// histKey is what makes two consecutive decisions "the same". Null means the
// event stands alone.
function histKey(e) {
  if ((e.memory && e.memory.length) || (e.signals && e.signals.length)) return null;
  const at = e.target ? `${e.target.x},${e.target.y}` : "";
  return [e.rule, e.action || "", e.reason || "", at, (e.matched || []).join(" ")].join("|");
}

function histEntry(e) {
  const node = el("div", "hist" + (e.idle ? " idle" : " acted"));
  const when = el("div", "when");
  const head = el("div", "head");
  head.append(el("span", "idx", ruleName(e)),
    el("span", "act", e.action || "idle"));
  // The cell the action aimed at. It cannot be worked out from the arena later:
  // the component or enemy it was aimed at has moved or is gone.
  if (e.target) head.append(el("span", "at", `→ ${e.target.x}, ${e.target.y}`));
  node.append(when, head);
  if (e.reason) node.append(el("div", "why", e.reason));

  // The rules that matched but did not take the tick. Without this the player
  // sees the winner and never the field it beat — and a rule of nothing but
  // memory writes runs every tick while looking completely dead.
  const also = (e.matched || []).filter((i) => i !== e.rule);
  if (also.length) {
    const hidden = (e.matched_total || 0) - (e.matched || []).length;
    node.append(el("div", "also", `also matched: ${also.map((i) => `rule ${i + 1}`).join(", ")}`
      + (hidden > 0 ? ` and ${hidden} more` : "")));
  }
  for (const m of e.memory || []) {
    node.append(el("div", "chip mem",
      m.cleared ? `cleared point ${m.point}` : `point ${m.point} set to ${m.x}, ${m.y}`));
  }
  for (const s of e.signals || []) {
    node.append(el("div", "chip sig", `heard “${s.kind}” from #${s.from} at ${s.x}, ${s.y}`));
  }
  const rest = (e.signals_total || 0) - (e.signals || []).length;
  if (rest > 0) node.append(el("div", "chip sig", `and ${rest} more signal${rest > 1 ? "s" : ""}`));

  // Text-only updates from here on: extending a run touches one text node.
  let to = e.tick, n = 1;
  const stamp = () => setText(when, n > 1 ? `ticks ${e.tick}–${to} · ${n} decisions` : `tick ${e.tick}`);
  stamp();
  return { node, extend: (next) => { to = next.tick; n++; stamp(); } };
}

function histAdd(events) {
  const list = $("history-list");
  // Prepending shifts everything down. Anyone who has scrolled away from the
  // top is reading something; keep it under their eye.
  const before = list.scrollHeight;
  for (const e of events) {
    const key = histKey(e);
    if (key !== null && histRun && histRun.key === key) { histRun.row.extend(e); continue; }
    const row = histEntry(e);
    list.insertBefore(row.node, list.firstChild);
    histRun = key === null ? null : { key, row };
    histRows++;
  }
  while (histRows > HISTORY_ROWS && list.lastChild) { list.lastChild.remove(); histRows--; }
  if (list.scrollTop > 0) list.scrollTop += list.scrollHeight - before;
}

function histReset() {
  $("history-list").replaceChildren();
  histSince = 0;
  histRun = null;
  histRows = 0;
  setText($("history-note"), selected === null
    ? "Select a robot to start recording why it acts."
    : `Recording robot #${selected} from now on.`);
}

// Polls are strictly serialised. Three things call this — the interval, a new
// selection, and the end frame — so two could otherwise be in flight with the
// same since=, and the slower reply would append ticks already on screen and
// wind histSince backwards.
async function pollHistory() {
  const robot = selected;
  if (robot === null || !matchID || histBusy) return;
  // Folded away: stop asking. Asking is what makes the server record, and the
  // watch registry is capped at 8 (internal/server/trace.go) — a panel nobody
  // has open must not hold one of them. Unfolding resumes from histSince, so it
  // needs no re-selection; the decisions taken while it was shut are simply not
  // recorded, which is the point.
  if (!$("p-history").open) return;
  histBusy = true;
  let data = null;
  try {
    data = await api("GET",
      `/api/matches/${encodeURIComponent(matchID)}/robots/${robot}/trace?since=${histSince}`);
  } catch { /* robot destroyed, match gone: keep what is on screen */ }
  finally { histBusy = false; }

  // A selection changed mid-flight would append another robot's decisions to
  // this one's list.
  if (!data || robot !== selected) return;
  const fresh = data.events.filter((e) => e.tick > histSince);
  if (fresh.length) {
    histSince = fresh[fresh.length - 1].tick;
    histAdd(fresh);
  }
  const secs = Math.round(data.window / (init?.tick_rate || 10));
  // A finished world never decides again, so "recording from now on" would be
  // a lie: what came back is the whole of what was kept.
  if (data.final) {
    setText($("history-note"), histRows === 0
      ? `The match is over, and robot #${robot} was not being recorded when it ended — a robot only records while somebody has it selected.`
      : `The last ${secs}s before the match ended, newest first.`);
    return;
  }
  setText($("history-note"), histRows === 0
    ? `Recording robot #${robot} from now on — history is only kept while a robot is selected.`
    : `Last ${secs}s of decisions, newest first. Only the selected robot is recorded.`);
}

setInterval(() => { if (!over) pollHistory(); }, HISTORY_POLL_MS);

// ---------------------------------------------------------------- commands
//
// Design §4.2: recall a robot, wait for it to walk home, install a program
// there. The server owns every rule — whose robot it is, whether it is at its
// base, whether the program fits the blueprint. The checks below only decide
// whether a control is worth showing; none of them is a security boundary.
//
// Nothing here mutates snapshot state optimistically. A command posts, and the
// next tick frame says what actually happened; a local guess would disagree
// with the arena drawn beside it.

let me = null;         // /api/me, only to find which colony is the viewer's
let programs = null;   // the player's library, null while it loads
let picked = "";       // chosen program id, kept across the per-tick re-render
let note = null;       // {robot, text, bad}: last command result for that robot
let cmd = null;        // the command controls, see commandBox
let cmdFor = null;     // robot they were built for

const myColony = () => {
  const seat = init?.colonies.find((c) => c.user_id === me?.id);
  return seat ? seat.id : null;
};

// sim.AtOwnBase: Chebyshev distance 1 of the colony's own base. Duplicated for
// the same reason as VISION_RANGE — the wire carries state, not rules — and it
// only greys a button out; the server still refuses an install in the field.
const atOwnBase = (r) => {
  const b = snap?.bases.find((x) => x.colony === r.colony);
  return !!b && Math.max(Math.abs(b.x - r.x), Math.abs(b.y - r.y)) <= 1;
};

async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: body ? { "Content-Type": "application/json" } : { Accept: "application/json" },
    body: body ? JSON.stringify(body) : undefined,
  });
  if (res.status === 401) { location.href = "/login"; return null; }
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    // An install refused by prog.Validate carries the offending rules, and each
    // message already names its rule. Keep them: "invalid program" on its own
    // gives the player nothing to fix.
    const issues = (data.issues || []).map((i) => i.message);
    throw new Error([data.error || res.statusText, ...issues].join(" · "));
  }
  return data;
}

async function loadPrograms() {
  const data = await api("GET", "/api/programs").catch(() => null);
  if (!data) return;
  programs = data.programs;
  render();
}

// commandBox builds the controls once per selected robot and returns an update
// hook instead of rebuilding them. The inspector re-renders on every tick, and
// a <select> replaced ten times a second cannot be used at all: the dropdown
// closes under the pointer.
function commandBox(r) {
  const node = el("div", "cmd");
  const state = el("div", "state");
  const recall = el("button", null, "Recall to base");
  const pick = el("select");
  const install = el("button", null, "Install");
  const msg = el("p", "note");
  const row = el("div", "row");
  row.append(pick, install);
  node.append(state, recall, row, msg);

  const fail = (e) => { note = { robot: r.id, text: e.message, bad: true }; };

  recall.addEventListener("click", async () => {
    note = null;
    recall.disabled = true; // until the next frame reports the flag
    try { await api("POST", `/api/matches/${matchID}/robots/${r.id}/recall`); }
    catch (e) { fail(e); }
    render();
  });

  pick.addEventListener("change", () => { picked = pick.value; });

  install.addEventListener("click", async () => {
    const id = Number(pick.value);
    if (!id) { note = { robot: r.id, text: "Pick a program first.", bad: true }; render(); return; }
    const name = pick.selectedOptions[0].textContent;
    note = null;
    install.disabled = true;
    try {
      const st = await api("POST", `/api/matches/${matchID}/robots/${r.id}/program`,
        { program_id: id });
      if (st) {
        // Counted from the server's own reply, not assumed: this is the claim
        // design §13 criterion 6 asks the player to verify.
        const cleared = st.memory.filter((p) => !p).length;
        note = { robot: r.id, text: `${name} installed — ${cleared} memory points cleared.` };
      }
    } catch (e) { fail(e); }
    render();
  });

  const update = (cur) => {
    const home = atOwnBase(cur);
    state.className = "state" + (cur.recalled ? (home ? " home" : " back") : "");
    state.textContent = cur.recalled
      ? (home ? "at base — awaiting program" : "returning to base")
      : "in the field, running its program";

    recall.disabled = over || cur.recalled;
    // A program can be installed on any robot standing at its own base, recalled
    // or not — that is the server's rule, and it is what a just-built robot needs.
    const ready = !over && home && programs !== null && programs.length > 0;
    pick.disabled = install.disabled = !ready;

    if (programs === null) {
      pick.replaceChildren(el("option", null, "loading library…"));
    } else if (pick.dataset.lib !== programs.map((p) => p.id).join(",")) {
      pick.dataset.lib = programs.map((p) => p.id).join(",");
      // A new library is seeded with the worked programs, so an empty one means
      // the player deleted them all; the editor is the only way back.
      const none = el("option", null,
        programs.length ? "— pick a program —" : "library empty — write one in the editor");
      none.value = ""; // without this an <option> takes its own text as its value
      pick.replaceChildren(none);
      for (const p of programs) {
        const o = el("option", null, p.name);
        o.value = String(p.id);
        pick.append(o);
      }
      pick.value = picked; // survives the re-render; empty if that program is gone
    }

    msg.className = "note" + (note && note.robot === cur.id ? (note.bad ? " bad" : " ok") : "");
    msg.textContent = note && note.robot === cur.id ? note.text : "";
  };

  return { node, update };
}

// ------------------------------------------------------------ elimination
//
// Design §5.3: the base is indestructible, but a colony with no robots left and
// no approved blueprint its base can cover is out of the match for good — it
// has no unit able to fetch another component, so its inventory can never
// change again, and a stock that covers nothing this tick covers nothing for
// the rest of the match. Calling that "idle" reads as a pause between builds.
//
// Derived here rather than sent: both facts are already on the wire. `robots`
// is the colony's own count from the snapshot, and the server sets idle_reason
// exactly when the base started no build this tick (sim.Base.IdleReason). A
// colony temporarily at zero robots whose inventory *does* cover a blueprint
// has a build running — b.build is set — and is deliberately not out.
const isOut = (colony) => {
  const st = snap?.colonies.find((c) => c.colony === colony);
  const b = snap?.bases.find((x) => x.colony === colony);
  return !!st && st.robots === 0 && !!b && !b.build && !!b.idle_reason;
};

// A player whose colony is out keeps watching (design §12 P2): spectating and
// the full trace history, only not the editing. None of that is enforced here —
// an out colony has no robot left for the server to accept a command on, and
// the command panel only ever appears for the viewer's own robots. This says
// out loud what happened, because a colony that quietly stops doing anything
// reads as a broken page rather than as a lost match.
function renderSpectate() {
  const box = $("spectate");
  const mine = myColony();
  const out = mine !== null && isOut(mine);
  box.hidden = !out;
  if (out) {
    setText(box, "Your colony is out — no robots left, and nothing your base can build. "
      + "You keep watching: pick any robot, from any colony, to follow what it decides.");
  }
}

function renderStats() {
  const t = $("stats");
  t.replaceChildren();
  if (!snap) return;
  const head = document.createElement("tr");
  for (const c of ["#", "Colony", "Robots", "Score", "Fleet", "Stock", "Parts", "Lost", "Kills"]) {
    head.append(el("th", null, c));
  }
  t.append(head);

  // Rank by score, not fleet value: the design §9 score is fleet value plus a
  // quarter of the base inventory, so a colony sitting on a large stock can
  // outrank one whose robots are worth more. Fleet value stays visible because
  // it is the term a player can watch moving on the board.
  const rows = [...snap.colonies].sort((a, b) => b.score - a.score || b.fleet_value - a.fleet_value);
  rows.forEach((c, i) => {
    const tr = document.createElement("tr");
    tr.append(el("td", null, String(i + 1)));
    const name = el("td", "who");
    const sw = el("span", "swatch");
    sw.style.background = colonyColor(c.colony);
    name.append(sw, document.createTextNode(colonyName(c.colony)));
    if (isOut(c.colony)) {
      tr.classList.add("outrow");
      name.append(el("span", "out", "out"));
      tr.title = "no robots left and nothing the base can build — design §5.3";
    }
    // Parts / Lost / Kills are sim.Stats, cumulative since tick 0 — what the
    // colony has *done*, next to what it currently holds. ticks_active has no
    // column of its own: it is one number about a colony that is already
    // described by its robot count, so it rides that cell as a tooltip.
    const robots = el("td", "num", String(c.robots));
    const secs = Math.round((c.ticks_active || 0) / (init?.tick_rate || 10));
    robots.title = `fielded a robot for ${secs}s of the match so far`;
    tr.append(name, robots,
      el("td", "num", String(c.score)), el("td", "num", String(c.fleet_value)),
      el("td", "num", String(c.inventory)), el("td", "num", String(c.collected ?? 0)),
      el("td", "num", String(c.losses ?? 0)), el("td", "num", String(c.kills ?? 0)));
    t.append(tr);
  });
}

// ------------------------------------------------------------------- graph
//
// Score over time (design §4.4), as one SVG polyline per colony. No charting
// library: this is a coordinate transform and a string, and a dependency here
// would buy axes nobody asked for.
//
// The series comes down on the init frame (internal/lobby/history.go) already
// downsampled, and grows from the tick stream — so nothing rides the 10 Hz
// frame, and a reload or a late join still shows the whole match.
//
// The graph is built once and updated in place, and it lives in the Colonies
// panel rather than in #inspector, which is cleared on every tick.

// GRAPH_MAX bounds the series in the browser the way historyCap bounds it on
// the server: once past it, every second sample is dropped and the interval
// doubles. A page left open on a long match must not grow without limit.
const GRAPH_MAX = 512;

const METRICS = { score: "score", robots: "robots", collected: "parts collected" };

let series = null; // {interval, ticks, colonies:[{colony, score, robots, collected}]}

function seriesReset(h) {
  series = h && Array.isArray(h.ticks) && h.interval > 0 ? h : null;
  drawGraph();
}

// seriesAppend adds this snapshot to the series if it lands on a sampling tick
// and is newer than what is there. Both guards matter: the init frame can be
// one sample ahead of the first tick frame (the server takes them under
// separate locks), and a reconnect replays a tick already recorded.
function seriesAppend(s) {
  if (!series || s.tick % series.interval !== 0) return false;
  const last = series.ticks.length ? series.ticks[series.ticks.length - 1] : -1;
  if (s.tick <= last) return false;
  series.ticks.push(s.tick);
  for (const c of series.colonies) {
    const st = s.colonies.find((x) => x.colony === c.colony) || {};
    c.score.push(st.score || 0);
    c.robots.push(st.robots || 0);
    c.collected.push(st.collected || 0);
  }
  if (series.ticks.length > GRAPH_MAX) {
    const half = (a) => a.filter((_, i) => i % 2 === 0);
    series.interval *= 2;
    series.ticks = half(series.ticks);
    for (const c of series.colonies) {
      c.score = half(c.score); c.robots = half(c.robots); c.collected = half(c.collected);
    }
  }
  return true;
}

const mmss = (ticks) => {
  const s = Math.round(ticks / (init?.tick_rate || 10));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
};

function drawGraph() {
  const lines = $("graph-lines");
  const note = $("graph-note");
  const metric = $("graph-metric").value;
  if (!series || series.ticks.length < 2) {
    lines.replaceChildren();
    setText(note, "Waiting for the first samples — one every "
      + `${mmss(series?.interval || 100)} of match time.`);
    return;
  }

  const W = 600, H = 180, pad = 6;
  const t0 = series.ticks[0];
  const span = Math.max(1, series.ticks[series.ticks.length - 1] - t0);
  let peak = 0;
  for (const c of series.colonies) for (const v of c[metric]) peak = Math.max(peak, v);
  const scale = peak || 1;

  lines.replaceChildren(...series.colonies.map((c) => {
    const p = document.createElementNS(SVGNS, "polyline");
    p.setAttribute("points", series.ticks.map((t, i) =>
      `${((t - t0) / span * W).toFixed(1)},`
      + `${(H - pad - (c[metric][i] || 0) / scale * (H - 2 * pad)).toFixed(1)}`).join(" "));
    // var(), not a resolved colour: the same custom property the map and the
    // legend read, so a theme switch needs no redraw.
    p.style.stroke = `var(${colonyVar(c.colony)})`;
    p.setAttribute("vector-effect", "non-scaling-stroke");
    const t = document.createElementNS(SVGNS, "title");
    t.textContent = colonyName(c.colony);
    p.append(t);
    return p;
  }));
  setText(note, `${METRICS[metric]} — peak ${peak}, ${mmss(t0)} to `
    + `${mmss(series.ticks[series.ticks.length - 1])}, `
    + `one point per ${mmss(series.interval)}. Colours match the standing above.`);
}

$("graph-metric").addEventListener("change", drawGraph);

// baseColony is the colony the base panel last showed. It sticks: without it
// the panel jumps back to the first base the instant the selected robot dies —
// which is exactly the moment its colony may be going out, and a colony with no
// robots left has nothing to select, so its base would be unreachable.
let baseColony = null;

function renderBase() {
  const box = $("base");
  box.replaceChildren();
  if (!snap) return;
  const sel = snap.robots.find((r) => r.id === selected);
  if (sel) baseColony = sel.colony;
  const b = snap.bases.find((x) => x.colony === baseColony) || snap.bases[0];
  if (!b) { box.append(el("p", "meta", "No base.")); return; }

  const head = el("div");
  const sw = el("span", "swatch");
  sw.style.background = colonyColor(b.colony);
  head.append(sw, el("strong", null, colonyName(b.colony)),
    el("span", "meta", ` · ${b.x}, ${b.y}`));
  box.append(head);

  box.append(el("h3", null, "Building"));
  if (b.build) {
    const key = `${b.colony}|${b.build.blueprint}`;
    const total = Math.max(buildTotals.get(key) || 0, b.build.ticks_left);
    buildTotals.set(key, total);
    const bar = el("div", "hp");
    const fill = el("i");
    fill.style.width = `${(1 - b.build.ticks_left / total) * 100}%`;
    bar.append(fill);
    box.append(el("div", null, b.build.blueprint), bar,
      el("div", "meta", `${b.build.ticks_left} ticks left · `
        + b.build.components.map(compName).join(", ")));
  } else if (isOut(b.colony)) {
    // Not idle: out. Design §5.3 — the base survives, the colony does not.
    const p = el("p", "meta");
    p.append(el("span", "out", "out"), document.createTextNode(
      ` — no robots left and ${b.idle_reason}. Nothing can fetch another`
      + " component, so this colony cannot build again."));
    box.append(p);
  } else {
    // idle_reason (from the server) distinguishes a base that is merely between
    // builds from one that is blocked — a silent stall reads as a bug.
    box.append(el("p", "meta", b.idle_reason ? `Idle — ${b.idle_reason}.` : "Idle."));
  }

  box.append(el("h3", null, "Inventory"));
  if (b.inventory.length === 0) box.append(el("p", "meta", "Empty."));
  else {
    const ul = el("ul", "tight");
    for (const e of b.inventory) ul.append(el("li", null, `${e.count} × ${compName(e.variant)}`));
    box.append(ul);
  }

  box.append(el("h3", null, "Approved blueprints"));
  const ul = el("ul", "tight");
  for (const bp of blueprintsOf(b.colony)) {
    ul.append(el("li", null, `${bp.name} — ${bp.components.map(compName).join(", ")} (${bp.value})`));
  }
  box.append(ul);
}

function renderClock() {
  if (!snap || !init) return;
  const left = Math.max(0, Number(snap.end_tick) - Number(snap.tick));
  const secs = Math.ceil(left / (init.tick_rate || 10));
  $("clock").textContent = `${Math.floor(secs / 60)}:${String(secs % 60).padStart(2, "0")}`;
  $("tick").textContent = `tick ${snap.tick} / ${snap.end_tick}`;
}

// -------------------------------------------------------------- folding
//
// The arena is the product; the five panels beside it are answers to questions
// a player has occasionally. They are <details>, which is native, needs no
// JavaScript to fold and puts the control in a <summary> — outside #inspector,
// which is replaceChildren()-ed ten times a second.
//
// This is only the memory: which panels are open survives a reload, so the
// choice is made once rather than on every visit. The defaults are in the HTML.
// The legend is in the set too, so it gets the same memory for free.
//
// localStorage throws rather than no-ops in a few configurations (private
// windows, third-party-cookie blocking on an embedded page). Losing the memory
// is acceptable; an uncaught exception on every toggle is not.
const store = {
  get(k) { try { return localStorage.getItem(k); } catch { return null; } },
  set(k, v) { try { localStorage.setItem(k, v); } catch { /* nothing to do */ } },
};

for (const d of document.querySelectorAll("details.fold")) {
  const key = `rc.fold.${d.id}`;
  const saved = store.get(key);
  if (saved !== null) d.open = saved === "1";
  d.addEventListener("toggle", () => store.set(key, d.open ? "1" : "0"));
}

// Unfolding the trace panel starts the poll again now rather than up to half a
// second from now: the panel is opened to answer a question about this tick.
$("p-history").addEventListener("toggle", () => { if (!over) pollHistory(); });

function render() {
  // A new selection starts a new history, and polls at once so the server
  // begins recording from this tick rather than from the next poll.
  if (selected !== histRobot) {
    histRobot = selected;
    histReset();
    pollHistory();
    // Picking a robot must visibly do something. A player who has folded the
    // inspector away and then clicks the arena would otherwise get no answer at
    // all; the fold is remembered, so this reopens it once and it stays open.
    if (selected !== null) $("p-selected").open = true;
  }
  // Only on a sampling tick, so the graph is rebuilt once every interval and
  // not ten times a second.
  if (snap && seriesAppend(snap)) drawGraph();
  draw();
  renderClock();
  renderSpectate();
  renderInspector();
  renderRoster();
  renderStats();
  renderBase();
}

// ---------------------------------------------------------------- stream

function conn(state, text) {
  const e = $("conn");
  e.className = `conn ${state}`;
  e.textContent = text;
}

let source = null;
let backoff = 1000;
let retryTimer = null;

function connect() {
  if (!matchID) { err("No match id in the URL: try /match?id=1"); conn("over", "no match"); return; }
  conn("retry", "connecting…");
  source = new EventSource(`/api/matches/${encodeURIComponent(matchID)}/stream`);

  source.addEventListener("open", () => {
    backoff = 1000;
    err("");
    conn("live", "live");
  });

  source.addEventListener("init", (ev) => {
    init = JSON.parse(ev.data);
    $("subtitle").textContent = `${init.name} — ${init.width}×${init.height}, seed ${init.seed}`;
    document.title = `${init.name} — robocolony`;
    styles.clear(); // silhouettes are resolved against this catalogue, not the last one
    bakeTerrain();
    buildLegend();
    // The server's series is authoritative and covers the whole match, so a
    // reconnect adopts it rather than keeping whatever this page observed.
    seriesReset(init.history);
    render();
  });

  source.addEventListener("tick", (ev) => {
    snap = JSON.parse(ev.data);
    render();
  });

  source.addEventListener("end", (ev) => {
    over = true;
    const e = JSON.parse(ev.data);
    source.close();
    source = null;
    pollHistory(); // the final ticks, before the poll loop stops for good
    // The last board and the standing stay on screen: freezing without saying
    // so is indistinguishable from a stalled simulation.
    render();
    conn("over", `match over at tick ${e.tick}`);
    $("clock").textContent = "0:00";
  });

  // EventSource retries on its own, but silently and forever, which looks
  // exactly like a stalled simulation. Drive it by hand so the state is visible.
  source.addEventListener("error", async () => {
    if (over || !source) return;
    source.close();
    source = null;
    conn("retry", "connection lost, checking…");

    // EventSource never reports the status code, so a dropped connection, an
    // expired session and a match that no longer exists all arrive identically.
    // The match endpoint is behind the same session and does report one, so one
    // probe tells them apart — and stops the retry loop on the two of them that
    // will never succeed. Live match state is in-memory (AGENTS.md), so a
    // bookmarked /match?id= after a restart is the expected way to hit 410.
    const res = await fetch(`/api/matches/${encodeURIComponent(matchID)}`,
      { headers: { Accept: "application/json" } }).catch(() => null);
    if (res && res.status === 401) { location.href = "/login"; return; }
    if (res && (res.status === 404 || res.status === 410)) {
      over = true;
      const body = await res.json().catch(() => ({}));
      err(body.error || "this match is not running");
      conn("over", "no match");
      return;
    }

    conn("retry", `reconnecting in ${Math.round(backoff / 1000)}s…`);
    clearTimeout(retryTimer);
    retryTimer = setTimeout(connect, backoff);
    backoff = Math.min(backoff * 2, 15000);
  });
}

connect();

// Which colony is the viewer's own. Only gates the command controls, so a
// failure here costs the buttons, not the observer view.
api("GET", "/api/me").then((u) => { me = u; render(); }).catch(() => {});
