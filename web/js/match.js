// Live match observer: canvas arena, robot inspector, colony stats.
//
// Renders strictly from the last snapshot the server sent. There is no
// interpolation and no client-side simulation: the server is authoritative, and
// a guess drawn between ticks would visibly disagree with the next frame.
//
// ?replay=1 points the same page at /api/matches/{id}/replay instead of the
// live stream. The frames are byte-identical, so everything below renders a
// finished match exactly as it renders a running one; the timeline gains a
// scrubber, and the two things that only make sense live — the trace poll and
// the command controls — stand down. See replayControls at the bottom.

import { colonyVar, drawGraph, mmss, seriesAppend, seriesReset, SVGNS } from "./graph.js";

import { SHAPES, MUZZLE, slug } from "./shapes.js";

const $ = (id) => document.getElementById(id);
const err = (m) => { $("err").textContent = m || ""; };

const params = new URLSearchParams(location.search);
const matchID = params.get("id");
const replay = params.get("replay") === "1";

// Heading is 0..7 clockwise from north (sim.Heading).
const HEADINGS = ["N", "NE", "E", "SE", "S", "SW", "W", "NW"];
const DELTA = [[0, -1], [1, -1], [1, 0], [1, 1], [0, 1], [-1, 1], [-1, 0], [-1, -1]];

// Forward vision, design §7.1 / sim.inCone: range 6 in Chebyshev distance and a
// 90° wedge (cos² of the half-angle is 1/2). Duplicated here on purpose — the
// wire carries state, not rules, and drawing the wrong cone is worse than
// drawing none.
const VISION_RANGE = 6;

// Radar reach, design §7.2 / sim.radar: omnidirectional, Chebyshev-ranged, and
// the range depends on which radar the robot carries (sim.radarRange,
// sim.baseRadarRange). Duplicated here for the same reason as VISION_RANGE —
// the wire carries state, not rules. Keyed by the slugged catalogue name, like
// SHAPES below, so a rename cannot leave a stale entry pointing at nothing; a
// radar this file has never heard of draws no mark at all, because a guessed
// reach is worse than none. web/radar_test.go fails when either drifts.
const RADAR_RANGE = {
  "parts-radar": 16,
  "enemy-robot-radar": 16,
  "enemy-base-radar": 28,
};

const css = (name, fallback) => {
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return v || fallback;
};

// Colony colours are resolved once a frame rather than once a robot: css() is a
// getComputedStyle call, and the arena, the minimap and the panels together ask
// for one per robot per frame with up to ~160 of them ten times a second. draw()
// empties this, so a theme switch still lands on the next frame with no reload.
const colonyColors = new Map();
const colonyColor = (id) => {
  let c = colonyColors.get(id);
  if (c === undefined) { c = css(colonyVar(id), "#888"); colonyColors.set(id, c); }
  return c;
};

// The name a robot goes by on screen: the initial of its archetype — which is
// the letter the arena draws inside it — and its id. The roster, the inspector
// and the overlay card all use it, so the list, the map and the panel agree on
// what to call the thing you picked.
const shortID = (r) => `${(r.archetype || "?").charAt(0).toUpperCase()}-${String(r.id).padStart(2, "0")}`;

const plural = (n, word) => `${n} ${word}${n === 1 ? "" : "s"}`;

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
//
// Nothing here touches canvas.style: the baked surface *is* the intrinsic size
// of the element, and match.html sizes the box off that. An inline max-width
// here used to say "never upscale", and it silently beat the stylesheet's
// max-width: 100% — which is the clamp that kept the arena inside its pane.
function bakeTerrain() {
  cell = Math.max(6, Math.min(24, Math.floor(900 / Math.max(init.width, init.height))));
  canvas.width = init.width * cell;
  canvas.height = init.height * cell;

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
  bakeMinimap();
}

// ---------------------------------------------------------------- minimap
//
// The whole arena at a glance (design 1a 215-226), in a card over the top right
// of the field. It is not a second renderer: the terrain is the offscreen
// canvas above, blitted down in one drawImage, and everything on top of it is a
// fillRect. Nothing here allocates, because it runs ten times a second.
//
// There is no viewport rectangle on it because there is no camera to draw one
// for — the arena always shows the whole world. Panning, zoom and the rectangle
// that would then mean something belong to the POV bead.

const MINIMAP_W = 148; // css px, and the surface too: it is never scaled by CSS

const minimap = $("minimap");
const mctx = minimap.getContext("2d");
let mcell = 0;

function bakeMinimap() {
  mcell = MINIMAP_W / init.width;
  minimap.width = MINIMAP_W;
  minimap.height = Math.round(init.height * mcell);
  setText($("minimap-label"), `${init.width} × ${init.height}`);
  $("minimap-card").hidden = false;
}

function drawMinimap() {
  mctx.drawImage(terrain, 0, 0, minimap.width, minimap.height);
  // A dot has to survive the scale: at 64 cells across a cell is barely two
  // pixels, and a base has to stay findable without becoming a district.
  const dot = Math.max(2, mcell);
  for (const b of snap.bases) {
    mctx.fillStyle = colonyColor(b.colony);
    mctx.fillRect(b.x * mcell - dot, b.y * mcell - dot, dot * 3, dot * 3);
  }
  for (const r of snap.robots) {
    mctx.fillStyle = colonyColor(r.colony);
    mctx.fillRect(r.x * mcell, r.y * mcell, dot, dot);
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
// The manipulator is already implied by the cargo dot, and the radars change
// what a robot knows rather than what it does.
//
// The paths themselves are in shapes.js, shared with the blueprint
// configurator: the same strings drive the canvas (through Path2D), the
// legend's inline SVG and the preview a player designs against, which is the
// only reason none of the three can drift from the others.
const BODY = Object.fromEntries(Object.entries(SHAPES).map(([k, d]) => [k, new Path2D(d)]));
const BARREL = new Path2D(MUZZLE);

// Silhouettes are cached per (colony, blueprint): up to ~160 robots are redrawn
// ten times a second and none of them may walk the base's blueprint list to do
// it. A blueprint id means one design for the whole match, so the entry never
// goes stale; the map is cleared with the rest of the match state on init.
const styles = new Map();
const UNKNOWN_STYLE = { shape: "unknown", armed: false, radar: 0, radarOf: "" };

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
  // Radar is a component (design §6.3: at most one), so presence and reach both
  // come out of the blueprint and a robot without one gets no mark. 0 means
  // "draws nothing", which is also what an unknown radar name gets.
  const radar = parts.find((c) => c && c.kind === "radar");
  const style = {
    shape: SHAPES[name] ? name : "unknown",
    armed: parts.some((c) => c && c.kind === "weapon"),
    radar: (radar && RADAR_RANGE[slug(radar.name)]) || 0,
    // Which radar, not just how far: the three of them answer three different
    // questions (design §7.2), and a contact count that did not know which
    // would be a number about nothing. Empty when none is fitted.
    radarOf: radar ? slug(radar.name) : "",
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

// SIGHT and RADAR, design 1a 192-193. These gate the two overlays and nothing
// else: hiding the wedge does not change what the robot perceives, it changes
// what is drawn over the ground it is standing on. The controls live in the
// field bar — see senseToggle — because #inspector is replaceChildren()-ed ten
// times a second and a button in there would be detached mid-click.
const senses = { sight: true, radar: true };

function draw() {
  if (!init || !terrain) return;
  colonyColors.clear();
  ctx.drawImage(terrain, 0, 0);
  if (!snap) return;
  weaponColor = css("--kind-weapon", "#c23b3b");

  const sel = snap.robots.find((r) => r.id === selected) || null;
  if (sel) {
    // Under the senses and everything else: the trail is where the robot has
    // been, and it must not sit on top of where it is.
    if (replay) drawTrail(sel);
    if (senses.sight) drawVision(sel);
    const { radar } = robotStyle(sel);
    if (radar && senses.radar) drawRadar(sel, radar);
  }

  for (const b of snap.bases) drawBase(b);
  for (const l of snap.loose) drawLoose(l);
  for (const r of snap.robots) drawRobot(r, r.id === selected);

  drawMinimap();
  renderSelCard(sel);
  renderTrailsCard(sel);
}

// inCone is sim.inCone: a 90° wedge — cos² of the half-angle is ½ — tested in
// the robot's own frame. The wedge the map paints and the count the overlay
// card reports both go through it, so the picture and the number cannot
// disagree. Range is the caller's business: it is Chebyshev, and drawVision
// expresses it as the bounds of its own loop.
function inCone(heading, dx, dy) {
  const [hx, hy] = DELTA[heading % 8];
  const dot = dx * hx + dy * hy;
  if (dot <= 0) return false;
  return dot * dot * 2 >= (dx * dx + dy * dy) * (hx * hx + hy * hy);
}

// drawVision paints the exact cells sim.inCone reports, not an approximate arc:
// the wedge is Chebyshev-ranged, so an arc would lie at the corners.
function drawVision(r) {
  ctx.fillStyle = colonyColor(r.colony);
  ctx.globalAlpha = .18;
  for (let dy = -VISION_RANGE; dy <= VISION_RANGE; dy++) {
    for (let dx = -VISION_RANGE; dx <= VISION_RANGE; dx++) {
      if (inCone(r.heading, dx, dy)) ctx.fillRect((r.x + dx) * cell, (r.y + dy) * cell, cell, cell);
    }
  }
  ctx.globalAlpha = 1;
}

// drawRadar outlines the box sim.radar actually sweeps. It is a square and not
// a circle for the same reason drawVision paints cells and not an arc: the
// range is Chebyshev, so the corner cell at (±range, ±range) is in reach and a
// circle would lie about it. Dashed and unfilled, so at a glance it reads as a
// second sense rather than more of the wedge.
function drawRadar(r, range) {
  const side = (2 * range + 1) * cell;
  ctx.save();
  ctx.strokeStyle = colonyColor(r.colony);
  ctx.lineWidth = 2;
  ctx.setLineDash([cell / 2, cell / 2]);
  ctx.strokeRect((r.x - range) * cell, (r.y - range) * cell, side, side);
  ctx.restore();
}

// ---------------------------------------------------------------- trails
//
// Where a robot has been (design 1c 524-535). The wire carries one tick at a
// time, so the path is remembered here rather than asked for: the server sends
// nothing extra for this.
//
// Replay only. Live, the head of the stream *is* the present — there is nothing
// behind it that a viewer has not just watched — and a 400-tick window for ~160
// robots would be paid for on every live frame to draw a picture nobody asked
// for. A seek clears the lot (see reopen): a buffer carried across a jump would
// draw a path through ticks that never followed one another.
//
// One ring buffer per robot, allocated the first time that robot is seen and
// never again. Appending is two writes and an index, so a tick costs nothing
// that grows with the window.
const TRAIL_TICKS = 400;
const trails = new Map(); // robot id -> {xs, ys, n, at}

function trailsRecord() {
  for (const r of snap.robots) {
    let t = trails.get(r.id);
    if (!t) {
      t = { xs: new Int16Array(TRAIL_TICKS), ys: new Int16Array(TRAIL_TICKS), n: 0, at: 0 };
      trails.set(r.id, t);
    }
    t.xs[t.at] = r.x;
    t.ys[t.at] = r.y;
    t.at = (t.at + 1) % TRAIL_TICKS;
    if (t.n < TRAIL_TICKS) t.n++;
  }
  // A robot that is gone has no path left to draw. Only walked when the counts
  // disagree, which is the tick something died on.
  if (trails.size > snap.robots.length) {
    const live = new Set(snap.robots.map((r) => r.id));
    for (const id of trails.keys()) if (!live.has(id)) trails.delete(id);
  }
}

// Dashed, in the robot's own colony colour and under everything else, so it
// reads as a record rather than as another thing on the board.
function drawTrail(r) {
  const t = trails.get(r.id);
  if (!t || t.n < 2) return;
  ctx.save();
  ctx.strokeStyle = colonyColor(r.colony);
  ctx.lineWidth = Math.max(1, cell * 0.14);
  ctx.setLineDash([cell / 2, cell / 2]);
  ctx.globalAlpha = .7;
  ctx.beginPath();
  const first = (t.at - t.n + TRAIL_TICKS) % TRAIL_TICKS;
  for (let i = 0; i < t.n; i++) {
    const j = (first + i) % TRAIL_TICKS;
    const x = t.xs[j] * cell + cell / 2, y = t.ys[j] * cell + cell / 2;
    if (i === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
  }
  ctx.stroke();
  ctx.restore();
}

function renderTrailsCard(r) {
  const card = $("trails-card");
  const t = r && trails.get(r.id);
  card.hidden = !replay || !t || t.n < 2;
  // The window is what has actually been recorded, not the cap: a replay opened
  // thirty ticks ago has thirty ticks of path, and saying 400 would be a claim
  // about frames this page never saw.
  if (!card.hidden) setText($("trails-label"), `Trails · last ${t.n} ticks · ${shortID(r)}`);
}

// --------------------------------------------------------- selected card
//
// The selected robot, said over the map it is standing on (design 1a 207-213):
// where it is, which way it faces, and what each of its two senses is reaching
// at this instant. The nodes are in match.html and only their text is written
// here, because this runs on every frame.
//
// The counts come from the same geometry the overlays are drawn from — inCone
// and the radar's Chebyshev box — so a number here can never describe a
// different shape from the one on the map.

function radarContacts(r, kind, range) {
  let list = null, enemyOnly = true;
  if (kind === "parts-radar") { list = snap.loose; enemyOnly = false; }
  else if (kind === "enemy-robot-radar") list = snap.robots;
  else if (kind === "enemy-base-radar") list = snap.bases;
  if (!list) return 0;
  let n = 0;
  for (const o of list) {
    if (enemyOnly && o.colony === r.colony) continue;
    if (Math.max(Math.abs(o.x - r.x), Math.abs(o.y - r.y)) <= range) n++;
  }
  return n;
}

function renderSelCard(r) {
  const card = $("sel-card");
  card.hidden = !r;
  if (!r) return;
  setText($("sel-who"), `Selected · ${shortID(r)}`);
  setText($("sel-cell"), `cell ${r.x},${r.y} · facing ${HEADINGS[r.heading % 8]}`);
  // var(--colony-N), never a resolved literal: the marks are then literally the
  // property the canvas reads, and they follow a theme switch with no re-render.
  const colour = `var(${colonyVar(r.colony)})`;

  const sight = $("sel-sight");
  sight.hidden = !senses.sight;
  if (senses.sight) {
    let seen = 0;
    for (const l of snap.loose) {
      const dx = l.x - r.x, dy = l.y - r.y;
      if (Math.max(Math.abs(dx), Math.abs(dy)) <= VISION_RANGE && inCone(r.heading, dx, dy)) seen++;
    }
    sight.firstElementChild.style.background = colour;
    setText(sight.lastElementChild,
      `sight · 90° × ${VISION_RANGE} cells · ${plural(seen, "component")}`);
  }

  const row = $("sel-radar");
  const { radar, radarOf } = robotStyle(r);
  row.hidden = !radar || !senses.radar;
  if (!row.hidden) {
    row.firstElementChild.style.color = colour;
    setText(row.lastElementChild, `radar · ${radar} cells omni · `
      + plural(radarContacts(r, radarOf, radar), "contact"));
  }
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
  // Bases share the one `best`, and they come second with a strict <, so the
  // genuinely nearest thing wins and a robot standing on its base still beats
  // the base underneath it.
  let base = null;
  for (const b of snap.bases) {
    const d = (b.x + .5 - px) ** 2 + (b.y + .5 - py) ** 2;
    if (d < best) { best = d; base = b; hit = null; }
  }
  if (base) {
    // A base has no selection state of its own: the inspector, the roster
    // highlight and the command panel all hang off `selected`, so clicking a
    // base means "select this colony" and is routed through one of its robots —
    // the one nearest the base, ties by id, so the same click twice does not
    // jump between two equidistant robots. An eliminated colony has none, and
    // then baseColony alone carries the panel (see renderBase).
    baseColony = base.colony;
    const d2 = (r) => (r.x - base.x) ** 2 + (r.y - base.y) ** 2;
    const rep = snap.robots.filter((r) => r.colony === base.colony)
      .sort((a, b) => d2(a) - d2(b) || a.id - b.id)[0];
    selected = rep ? rep.id : null;
    // The Base panel is closed by default, so without this the click looks like
    // a no-op — the same reason render() reopens #p-selected on a robot pick.
    $("p-base").open = true;
    $("p-base").scrollIntoView({ block: "nearest" });
  } else {
    selected = hit ? hit.id : null;
  }
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

// idCard is the inspector's header, design 261-266: the colony as a swatch, the
// robot's short id, its archetype, and — right-aligned, because it is the fact
// that decides whether everything under it is present tense — whether it is
// still in the world.
function idCard(r, alive) {
  const head = el("div", "idcard");
  if (r) {
    const sw = el("span", "swatch");
    sw.style.background = colonyColor(r.colony);
    sw.title = colonyName(r.colony);
    head.append(sw, el("span", "id", shortID(r)), el("span", "kind", r.archetype));
  } else {
    head.append(el("span", "id", `#${selected}`));
  }
  head.append(el("span", alive ? "badge" : "badge gone", alive ? "▮ Alive" : "▮ Destroyed"));
  return head;
}

// The last robot the inspector drew. A snapshot that no longer carries it
// carries nothing to name it by either, and "#7 is gone" is a worse answer than
// "S-07 is gone" when the whole panel is about which robot this was.
let lastSel = null;

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
    box.append(idCard(lastSel && lastSel.id === selected ? lastSel : null, false),
      el("p", "meta", `Robot #${selected} is gone — destroyed, or salvaged.`));
    renderCommand(null);
    return;
  }
  lastSel = r;

  box.append(idCard(r, true));

  const frac = r.hp_max > 0 ? r.hp / r.hp_max : 0;
  const bar = el("div", "hp" + (frac <= .33 ? " crit" : frac < 1 ? " hurt" : ""));
  const fill = el("i");
  fill.style.width = `${Math.max(0, Math.min(1, frac)) * 100}%`;
  bar.append(fill);
  // The fraction goes above the bar (design 269-277): the number is the fact,
  // and the bar is the shape of it.
  const hphead = el("div", "hp-head");
  hphead.append(el("span", "label", "Health"), el("span", "v", `${r.hp} / ${r.hp_max}`));
  box.append(hphead, bar);

  box.append(el("h3", null, "Active rule"));
  box.append(ruleBox(r));

  // Loadout is what the robot *is* (design 303-310). Where it is standing and
  // which way it faces moved to the card over the map, where the cell it names
  // is a place you can look at rather than a pair of numbers.
  const bp = blueprintOf(r);
  box.append(el("h3", null, "Loadout"));
  box.append(defs([
    ["Blueprint", bp ? `${bp.name} (${bp.id})` : r.blueprint],
    ["Parts", bp ? bp.components.map(compName).join(", ") : "—"],
    ["Program", r.program || "none"],
    ["Cargo", r.cargo ? compName(r.cargo) : "empty"],
    ["Mass", bp ? massLine(bp) : "—"],
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
  // Nothing is commandable in a replay: the match is over, and the server would
  // refuse every one of these — offering the buttons anyway reads as a bug.
  if (!r || replay || r.colony !== myColony()) {
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
  // The cooldown is about this tick's decision — whether the weapon was even
  // available to it — so it belongs beside the decision rather than in the
  // loadout, which is what the robot is rather than what it can do right now.
  if (r.cooldown > 0) div.append(el("div", "meta", `weapon cooling down · ${r.cooldown} ticks`));
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

// massLine is design 309: what the robot weighs, and what that weight costs it.
//
// The mass is added up here because it is addition over a catalogue the init
// frame already carries. The pace is not: design §6.4's speed model is sim's,
// and E7.3 retunes the game by editing sim's tables, so nothing outside that
// package may hold a second copy of them (internal/server/programs.go). It is
// asked for once per parts list and remembered — the row says the mass alone
// until the answer lands, and keeps saying it if the request fails, which is
// what an observer without a library gets.
const paces = new Map(); // parts list -> ticks per cell on open ground

function massLine(bp) {
  const mass = bp.components.reduce((n, v) => n + (catalogue(v)?.mass || 0), 0);
  const key = bp.components.join(",");
  if (!paces.has(key)) {
    paces.set(key, null); // in flight, and never asked for twice
    api("POST", "/api/blueprints/preview", { components: bp.components })
      .then((s) => {
        if (!s || !s.ticks_per_cell) return;
        paces.set(key, s.ticks_per_cell);
        render();
      })
      .catch(() => { /* the mass is still true without it */ });
  }
  const pace = paces.get(key);
  return pace ? `${mass} · 1 cell / ${pace} ticks` : String(mass);
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

const rosterRows = new Map();   // robot id -> {node, update}
const rosterGroups = new Map(); // colony -> {node, update}
const rosterOrder = [];         // robot ids in list order, for ↑/↓
let rosterShown = null;         // selection the list last scrolled to

const setText = (n, s) => { if (n.textContent !== s) n.textContent = s; };

// A colony's header row (design 86-96, 153-170). The swatch that used to repeat
// on every row is said once here, with the colony's name, how many robots it
// has left, and the YOU tag on the viewer's own.
function rosterGroup(colony) {
  const node = el("div", "group");
  const sw = el("span", "swatch");
  // var(--colony-N) rather than a resolved colour: the header is built once and
  // lives as long as the colony does, so a theme switch has to reach it.
  sw.style.background = `var(${colonyVar(colony)})`;
  const count = el("span", "meta");
  const you = el("span", "meta", "you");
  node.append(sw, el("span", "name", colonyName(colony)), count, el("span", "grow"), you);
  return {
    node,
    update: (n, mine) => { setText(count, `· ${n}`); you.hidden = !mine; },
  };
}

function rosterRow(r) {
  const node = el("button", "row");
  node.type = "button";
  // Identity is fixed at build time; only the fields below change per tick.
  const who = el("span", "who", shortID(r));
  const act = el("span", "act");
  const hp = el("span", "num");
  node.append(who, act, hp);
  node.addEventListener("click", () => { selected = r.id; render(); });

  const update = (cur) => {
    // One health number, not a fraction: the row is scanned, and hurt and
    // critical are already said in colour (design 99-151).
    setText(hp, String(cur.hp));
    hp.className = cur.hp * 3 <= cur.hp_max ? "num crit" : cur.hp < cur.hp_max ? "num hurt" : "num";

    // A recalled robot has suspended its program (design §4.2), so its trace is
    // stale — say what it is actually doing, not what it last decided.
    const t = cur.trace;
    const doing = !cur.recalled && (!t || !t.action);
    setText(act, cur.recalled ? "returning to base"
      : t ? (t.action || (t.rule >= 0 ? "idle" : "no rule matched — idle"))
        : "no decision yet");
    act.className = doing ? "act idle" : "act";

    // Cargo lost its column to the single-line row, so it rides the tooltip
    // with the reason — the arena still draws the shoulder dot for it.
    let why = cur.recalled ? "recalled" : (t && t.reason) || "";
    if (cur.cargo) why = why ? `${why} · carrying ${compName(cur.cargo)}` : `carrying ${compName(cur.cargo)}`;
    if (node.title !== why) node.title = why;

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
  // belongs: in the steady state this touches no DOM at all. Group headers are
  // part of that walk — they are keyed by colony, and both keys are fixed for
  // as long as the thing they name exists, so nothing moves under the pointer.
  const mine = myColony();
  rosterOrder.length = 0;
  let at = list.firstChild;
  let colony = null;
  for (const r of live) {
    if (r.colony !== colony) {
      colony = r.colony;
      let group = rosterGroups.get(colony);
      if (!group) { group = rosterGroup(colony); rosterGroups.set(colony, group); }
      if (at === group.node) at = at.nextSibling;
      else list.insertBefore(group.node, at);
      // The server's own count, not one recomputed here: it is on every frame.
      group.update(snap.colonies.find((c) => c.colony === colony)?.robots ?? 0, colony === mine);
    }
    let row = rosterRows.get(r.id);
    if (!row) { row = rosterRow(r); rosterRows.set(r.id, row); }
    if (at === row.node) at = at.nextSibling;
    else list.insertBefore(row.node, at);
    row.update(r);
    rosterOrder.push(r.id);
  }

  const ids = new Set(live.map((r) => r.id));
  for (const [id, row] of rosterRows) {
    if (!ids.has(id)) { row.node.remove(); rosterRows.delete(id); }
  }
  const colonies = new Set(live.map((r) => r.colony));
  for (const [id, group] of rosterGroups) {
    if (!colonies.has(id)) { group.node.remove(); rosterGroups.delete(id); }
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
  const when = el("span", "when");
  const head = el("div", "head");
  head.append(el("span", "idx", ruleName(e)),
    el("span", "act", e.action || "idle"));
  // The cell the action aimed at. It cannot be worked out from the arena later:
  // the component or enemy it was aimed at has moved or is gone.
  if (e.target) head.append(el("span", "at", `→ ${e.target.x}, ${e.target.y}`));
  // The tick range is the last thing on the head row, right-aligned (design
  // 337-339): the rule and its action are what the eye runs down, and when it
  // happened is the column it checks them against.
  head.append(when);
  node.append(head);
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
    node.append(el("div", "evt mem",
      m.cleared ? `cleared point ${m.point}` : `point ${m.point} set to ${m.x}, ${m.y}`));
  }
  for (const s of e.signals || []) {
    node.append(el("div", "evt sig", `heard “${s.kind}” from #${s.from} at ${s.x}, ${s.y}`));
  }
  const rest = (e.signals_total || 0) - (e.signals || []).length;
  if (rest > 0) node.append(el("div", "evt sig", `and ${rest} more signal${rest > 1 ? "s" : ""}`));

  // Text-only updates from here on: extending a run touches one text node.
  let n = 1;
  const stamp = () => setText(when, n > 1 ? `${e.tick} · ×${n}` : String(e.tick));
  stamp();
  return { node, extend: () => { n++; stamp(); } };
}

function histAdd(events) {
  const list = $("history-list");
  // Prepending shifts everything down. Anyone who has scrolled away from the
  // top is reading something; keep it under their eye.
  const before = list.scrollHeight;
  for (const e of events) {
    const key = histKey(e);
    if (key !== null && histRun && histRun.key === key) { histRun.row.extend(); continue; }
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
  setText($("history-note"), replay
    ? "Decisions are recorded while a match runs, and only for a selected robot,"
    + " so a replay has none to show."
    : selected === null
      ? "Select a robot to start recording why it acts."
      : `Recording robot #${selected} from now on.`);
}

// Polls are strictly serialised. Three things call this — the interval, a new
// selection, and the end frame — so two could otherwise be in flight with the
// same since=, and the slower reply would append ticks already on screen and
// wind histSince backwards.
async function pollHistory() {
  const robot = selected;
  // A replayed match decides nothing: asking is what makes the server record,
  // and there is no live world to record from. histReset says so on screen.
  if (robot === null || !matchID || histBusy || replay) return;
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
  // Design 321-328: two equal buttons side by side and one caption under them.
  // The program picker sits above the pair rather than behind an "INSTALL
  // PROGRAM…" dialog — a dialog would be a second interactive thing hanging off
  // a panel that is rebuilt ten times a second, and this box already exists
  // outside #inspector for exactly that reason.
  const node = el("div", "cmd");
  const state = el("div", "state");
  const pick = el("select");
  const recall = el("button", "btn sm", "Recall home");
  const install = el("button", "btn sm", "Install program");
  const msg = el("p", "note");
  const row = el("div", "row");
  row.append(recall, install);
  node.append(state, pick, row,
    el("p", "caption", "Reprogramming wipes all three memory points."
      + " It takes effect on the next tick."),
    msg);

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
  const rate = init.tick_rate || 10;
  const end = Number(snap.end_tick);
  const left = Math.max(0, end - Number(snap.tick));
  const secs = Math.ceil(left / rate);
  setText($("clock"), `${Math.floor(secs / 60)}:${String(secs % 60).padStart(2, "0")}`);
  // Design 76 and 515: the big number is the time left, and one meta line says
  // what it is of. In a replay that line becomes how far behind the running
  // match you are — but only when the client has been told where that is; see
  // liveTick. Everywhere else it would be a made-up number.
  setText($("tick"), liveTick === null
    ? `/ ${mmss(end)} · tick ${snap.tick}`
    : `tick ${snap.tick} · −${Math.max(0, Math.round((liveTick - Number(snap.tick)) / rate))}s`);

  // The timeline is one filled bar whose right edge is now. Live it is not a
  // scrubber — nothing behind the head is seekable, because the server keeps no
  // per-tick history to seek into. A replay has the whole log, so the scrubber
  // under the bar is shown for that case only.
  const done = end > 0 ? Math.min(1, Number(snap.tick) / end) : 0;
  $("progress").style.width = `${(done * 100).toFixed(2)}%`;
  // The chip rides the head, so it needs no position of its own. Live it says
  // NOW; a replay is somewhere in the past and says which tick (design 252, 560).
  setText($("now-chip"), replay ? `tick ${snap.tick}` : "now");
  const mark = $("live-mark");
  mark.hidden = liveTick === null || end <= 0;
  if (!mark.hidden) {
    mark.style.left = `${Math.min(100, liveTick / end * 100).toFixed(2)}%`;
    setText($("live-mark-label"), `live ${liveTick}`);
  }
  const gone = Math.floor(Number(snap.tick) / rate);
  setText($("tick-mid"), `${Math.floor(gone / 60)}:${String(gone % 60).padStart(2, "0")} elapsed`);
  if (replay) renderSeek(end);
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

// -------------------------------------------------------------- field bar
//
// The two sense toggles. They read their own label out of the markup and put
// the tick or the cross back, so the word is written once — in match.html,
// where the title that explains it lives too. [aria-pressed] is the whole
// state: what a screen reader announces cannot drift from the box that is drawn
// because they are the same attribute.
function senseToggle(btn, key) {
  const name = btn.textContent.trim().replace(/\s*[✓✗]$/, "");
  const sync = () => {
    btn.setAttribute("aria-pressed", senses[key] ? "true" : "false");
    setText(btn, `${name} ${senses[key] ? "✓" : "✗"}`);
  };
  // draw() alone: nothing outside the canvas and its overlay card reads these.
  btn.addEventListener("click", () => { senses[key] = !senses[key]; sync(); draw(); });
  sync();
}
senseToggle($("t-sight"), "sight");
senseToggle($("t-radar"), "radar");

// -------------------------------------------------------------- keyboard
//
// ↑/↓ walk the roster in the order it is drawn (design 177-180); ←/→ step a
// replay one tick (design 550). Both are ignored while a form control has the
// focus — the program picker is a <select>, and stealing its arrow keys would
// make it unusable — and while a modifier is down, which is the browser's.
document.addEventListener("keydown", (ev) => {
  if (ev.altKey || ev.ctrlKey || ev.metaKey || ev.shiftKey) return;
  if (ev.target instanceof Element && ev.target.closest("input, select, textarea")) return;

  if (ev.key === "ArrowUp" || ev.key === "ArrowDown") {
    if (rosterOrder.length === 0) return;
    ev.preventDefault();
    const step = ev.key === "ArrowDown" ? 1 : -1;
    const i = rosterOrder.indexOf(selected);
    // No selection yet starts at the end you are walking from.
    selected = i < 0
      ? rosterOrder[step > 0 ? 0 : rosterOrder.length - 1]
      : rosterOrder[(i + step + rosterOrder.length) % rosterOrder.length];
    render();
    return;
  }

  if (replay && (ev.key === "ArrowLeft" || ev.key === "ArrowRight")) {
    // One tick is one reconnect, like every other replay control: the client
    // keeps no buffer to nudge, and the server rebuilds to the target.
    ev.preventDefault();
    reopen(at() + (ev.key === "ArrowRight" ? 1 : -1));
  }
});

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

// Replay state. `from` is the tick the next connection starts at: it follows
// the stream while playing, so a dropped connection resumes where it stopped
// rather than at the last seek.
let from = 0;
let speed = 1;
let seeking = false; // connected, first frame of this seek not in yet
let dragging = false;

// Where the running match has got to, or null when there is no running match to
// be behind — which is the ordinary case, because a replay is a *finished*
// match played back from its command log. It is asked for once, in replay mode
// only (see probeLive): a number that ages by a tick every 100ms is a marker on
// a ten-minute timeline, not a clock, and polling it would buy nothing. Null is
// what keeps the "−131s" readout, the LIVE marker and the JUMP TO LIVE button
// off the screen rather than making any of them up.
let liveTick = null;

function connect() {
  if (!matchID) { err("No match id in the URL: try /match?id=1"); conn("over", "no match"); return; }
  seeking = replay;
  conn("retry", replay ? "seeking…" : "connecting…");
  source = new EventSource(replay
    ? `/api/matches/${encodeURIComponent(matchID)}/replay?from=${from}&speed=${speed}`
    : `/api/matches/${encodeURIComponent(matchID)}/stream`);

  source.addEventListener("open", () => {
    backoff = 1000;
    err("");
    // A replay says nothing here. The server rebuilds the world from tick 0
    // before the first frame — a few hundred ms on a default match — and
    // "live" over a board that has not moved yet would be a lie. The tick
    // handler reports it when there is something to report.
    if (!replay) conn("live", "▮ live");
  });

  source.addEventListener("init", (ev) => {
    init = JSON.parse(ev.data);
    // Design 70-73. The name is the match's, the two badges are this one's:
    // which match it is, and the seed its world was generated from — the number
    // a player quotes when the terrain is worth talking about. The size moved to
    // the minimap's label, where it is beside the thing it measures.
    setText($("subtitle"), init.name);
    setText($("match-no"), `Match #${init.match_id}`);
    setText($("match-seed"), `Seed ${init.seed}`);
    $("match-no").hidden = false;
    $("match-seed").hidden = false;
    document.title = `${init.name} — robocolony`;
    styles.clear(); // silhouettes are resolved against this catalogue, not the last one
    bakeTerrain();
    buildLegend();
    // The server's series is authoritative and covers the whole match, so a
    // reconnect adopts it rather than keeping whatever this page observed.
    seriesReset(init.history, init.tick_rate, colonyName);
    render();
  });

  source.addEventListener("tick", (ev) => {
    snap = JSON.parse(ev.data);
    if (replay) {
      from = Number(snap.tick);
      // One frame per tick, in order, so this is the whole of the trail: a seek
      // clears it rather than stitching two stretches of match together.
      trailsRecord();
      if (seeking) { seeking = false; conn("live", `▮ replay ×${speed}`); }
    }
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
    //
    // A replay asks the history endpoint instead, for the same reasons plus
    // one: /api/matches/{id} reports 410 for every finished match, which is
    // exactly the match a replay is watching. The history record also carries
    // the refusal — a build that no longer simulates the same way — which the
    // stream can only answer with a 409 EventSource will not show us.
    const res = await fetch(replay
      ? `/api/history/${encodeURIComponent(matchID)}`
      : `/api/matches/${encodeURIComponent(matchID)}`,
      { headers: { Accept: "application/json" } }).catch(() => null);
    if (res && res.status === 401) { location.href = "/login"; return; }
    if (replay && res && res.ok) {
      const body = await res.json().catch(() => ({}));
      if (body.replayable === false) {
        over = true;
        err(body.reason || "this match cannot be replayed by this build");
        conn("over", "not replayable");
        return;
      }
    }
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

// ---------------------------------------------------------------- replay
//
// Every control is a reconnect. Pause closes the EventSource; play, scrub and
// speed reopen /api/matches/{id}/replay with new from/speed, and the server
// rebuilds the world from tick 0 to reach the target. That is the design, not
// a shortcut: the client keeps no clock, no buffer and no playback state, so
// there is nothing that can fall out of step with the server, and the protocol
// stays the three events the live stream already sends.
//
// The controls sit in the timeline under the arena. Not in #inspector, which is
// replaceChildren()-ed ten times a second — that would close the speed select
// under the pointer (docs/engineering-notes.md).

const syncPlay = () => { setText($("rp-play"), source ? "Pause" : "Play"); };

function pause() {
  if (source) { source.close(); source = null; }
  clearTimeout(retryTimer);
  conn("over", "paused");
  syncPlay();
}

// at is where the replay currently stands: the last frame that arrived, or the
// tick the next connection is queued to start from while it is paused.
const at = () => Number(snap ? snap.tick : from);

// Ten seconds a press. Enough to move, small enough that the rebuild the server
// does for it is the same order of work as an ordinary scrub.
const skipTicks = () => 10 * (init?.tick_rate || 10);

// reopen is play, seek, skip and speed change alike: one connection, one tick.
function reopen(atTick) {
  if (source) { source.close(); source = null; }
  clearTimeout(retryTimer);
  from = Math.max(0, Math.round(atTick));
  // A trail is a run of consecutive ticks. Across a jump it would draw a path
  // through positions that never followed one another, so it starts again here.
  trails.clear();
  // The end frame set `over`, which stops the retry loop. Scrubbing back out of
  // it is how a replay gets watched twice.
  over = false;
  connect();
  syncPlay();
}

// renderSeek tracks the incoming tick, unless the player has the thumb.
function renderSeek(end) {
  const seek = $("rp-seek");
  if (seek.max !== String(end)) seek.max = String(end);
  if (!dragging) {
    seek.value = String(snap.tick);
    setText($("rp-at"), `${mmss(Number(snap.tick))} / ${mmss(end)}`);
  }
  syncPlay();
}

// probeLive asks, once, whether there is still a live match behind this replay.
// There usually is not — a replay is a finished match played back from its
// command log — and then nothing here appears at all: no jump, no LIVE marker,
// no "−131s". The alternative to asking is inventing one of them.
//
// Plain fetch rather than api(): a 401 here is a signed-out observer watching a
// public replay, and bouncing them to the login page over an optional badge
// would be a worse answer than not showing it.
async function probeLive() {
  const res = await fetch(`/api/matches/${encodeURIComponent(matchID)}`,
    { headers: { Accept: "application/json" } }).catch(() => null);
  if (!res || !res.ok) return; // 404 or 410: finished, or gone with the process
  const info = await res.json().catch(() => null);
  if (!info || info.state !== "running" || !Number.isFinite(Number(info.tick))) return;
  liveTick = Number(info.tick);
  const jump = $("to-live");
  jump.href = `/match?id=${encodeURIComponent(matchID)}`;
  jump.hidden = false;
  render();
}

if (replay) {
  $("replay").hidden = false;
  $("replay-badge").hidden = false;
  setText($("timeline-note"), "a finished match, replayed from its command log");
  probeLive();
  $("rp-play").addEventListener("click", () => {
    if (source) pause(); else reopen(Number($("rp-seek").value));
  });
  // Skip is a seek by a fixed step, so it goes through the same reconnect.
  $("rp-back").addEventListener("click", () => reopen(at() - skipTicks()));
  $("rp-fwd").addEventListener("click", () => reopen(at() + skipTicks()));
  // Dragging pauses: incoming ticks write the thumb's position, so a stream
  // left running would fight the pointer for it.
  $("rp-seek").addEventListener("input", () => {
    dragging = true;
    if (source) pause();
    setText($("rp-at"), `${mmss(Number($("rp-seek").value))} / ${mmss(Number($("rp-seek").max))}`);
  });
  $("rp-seek").addEventListener("change", () => {
    dragging = false;
    reopen(Number($("rp-seek").value));
  });
  // The speed set is the server's own clamp (internal/server/replay.go: 0.25 to
  // 16), so no button here can ask for a rate that is silently rounded. One
  // listener on the group rather than four on the buttons, and [aria-pressed]
  // is the selection — the same attribute app.css draws the segment from.
  $("rp-speed").addEventListener("click", (ev) => {
    const btn = ev.target.closest("button[data-speed]");
    if (!btn) return;
    speed = Number(btn.dataset.speed) || 1;
    for (const b of $("rp-speed").children) {
      b.setAttribute("aria-pressed", b === btn ? "true" : "false");
    }
    if (source) reopen(at()); // paused stays paused; the next play uses the new speed
  });
  syncPlay();
}

connect();

// Which colony is the viewer's own. Only gates the command controls, so a
// failure here costs the buttons, not the observer view.
api("GET", "/api/me").then((u) => { me = u; render(); }).catch(() => {});
