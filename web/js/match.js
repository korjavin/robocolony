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
const colonyColor = (id) => css(`--colony-${((id % 8) + 8) % 8}`, "#888");
const slug = (s) => String(s).toLowerCase().replace(/[^a-z0-9]+/g, "-");

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

  const colors = init.terrain_legend.map((n) => css(`--terrain-${slug(n)}`, css("--terrain-unknown", "#888")));
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

// ---------------------------------------------------------------- drawing

function draw() {
  if (!init || !terrain) return;
  ctx.drawImage(terrain, 0, 0);
  if (!snap) return;

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
  const cx = r.x * cell + cell / 2, cy = r.y * cell + cell / 2, rad = cell * 0.4;
  ctx.fillStyle = colonyColor(r.colony);
  ctx.beginPath();
  ctx.arc(cx, cy, rad, 0, Math.PI * 2);
  ctx.fill();

  // Heading notch. Forward vision is the whole game: a robot whose facing you
  // cannot see is a robot whose program you cannot debug.
  const [hx, hy] = DELTA[r.heading % 8];
  const n = Math.hypot(hx, hy) || 1;
  ctx.strokeStyle = "#000";
  ctx.lineWidth = Math.max(1.5, cell * 0.16);
  ctx.beginPath();
  ctx.moveTo(cx, cy);
  ctx.lineTo(cx + (hx / n) * cell * 0.75, cy + (hy / n) * cell * 0.75);
  ctx.stroke();

  // Archetype glyph: first letter of the blueprint name, which is player-chosen
  // and therefore cannot come from a hardcoded table.
  glyph((r.archetype || "?").charAt(0).toUpperCase(), r.x, r.y, cell * 0.65);

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
  if (selected === null) { box.append(el("p", "meta", "No robot selected.")); return; }
  const r = snap?.robots.find((x) => x.id === selected);
  if (!r) {
    box.append(el("p", "meta", `Robot #${selected} is gone — destroyed, or salvaged.`));
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

  if (r.colony === myColony()) {
    box.append(el("h3", null, "Command"));
    if (cmdFor !== r.id) { // rebuilt only on a new selection, see commandBox
      note = null;
      cmd = commandBox(r);
      cmdFor = r.id;
      loadPrograms();
    }
    cmd.update(r);
    box.append(cmd.node);
  }
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
  div.append(el("div", "idx", t.rule < 0 ? "no rule matched" : `rule ${t.rule + 1}`));
  div.append(el("div", "act", t.action ? t.action : "idle"));
  div.append(el("div", "why", t.reason || ""));
  if (snap && t.tick !== snap.tick) {
    div.append(el("div", "meta", `decided on tick ${t.tick}, ${snap.tick - t.tick} ticks ago`));
  }
  return div;
}

function blueprintOf(r) {
  for (const b of snap.bases) {
    if (b.colony !== r.colony) continue;
    const bp = b.blueprints.find((x) => x.id === r.blueprint);
    if (bp) return bp;
  }
  return null;
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
  head.append(el("span", "idx", e.rule < 0 ? "no rule matched" : `rule ${e.rule + 1}`),
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
      const none = el("option", null, programs.length ? "— pick a program —" : "library is empty");
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

function renderStats() {
  const t = $("stats");
  t.replaceChildren();
  if (!snap) return;
  const head = document.createElement("tr");
  for (const c of ["#", "Colony", "Robots", "Score", "Fleet value", "Parts"]) head.append(el("th", null, c));
  t.append(head);

  // Rank by score, not fleet value: the design §9 score is fleet value plus a
  // quarter of the base inventory, so a colony sitting on a large stock can
  // outrank one whose robots are worth more. Fleet value stays visible because
  // it is the term a player can watch moving on the board.
  const rows = [...snap.colonies].sort((a, b) => b.score - a.score || b.fleet_value - a.fleet_value);
  rows.forEach((c, i) => {
    const tr = document.createElement("tr");
    tr.append(el("td", null, String(i + 1)));
    const name = el("td");
    const sw = el("span", "swatch");
    sw.style.background = colonyColor(c.colony);
    name.append(sw, document.createTextNode(colonyName(c.colony)));
    tr.append(name, el("td", null, String(c.robots)), el("td", null, String(c.score)),
      el("td", null, String(c.fleet_value)), el("td", null, String(c.inventory)));
    t.append(tr);
  });
}

function renderBase() {
  const box = $("base");
  box.replaceChildren();
  if (!snap) return;
  const sel = snap.robots.find((r) => r.id === selected);
  const colony = sel ? sel.colony : snap.bases[0]?.colony;
  const b = snap.bases.find((x) => x.colony === colony);
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
  for (const bp of b.blueprints) {
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

function render() {
  // A new selection starts a new history, and polls at once so the server
  // begins recording from this tick rather than from the next poll.
  if (selected !== histRobot) {
    histRobot = selected;
    histReset();
    pollHistory();
  }
  draw();
  renderClock();
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
    bakeTerrain();
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
