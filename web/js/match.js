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

canvas.addEventListener("click", (ev) => {
  if (!init || !snap) return;
  const box = canvas.getBoundingClientRect();
  const x = Math.floor((ev.clientX - box.left) / box.width * init.width);
  const y = Math.floor((ev.clientY - box.top) / box.height * init.height);
  const hit = snap.robots.find((r) => r.x === x && r.y === y);
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
  const mem = el("ul", "tight");
  (r.memory || []).forEach((p, i) => {
    mem.append(el("li", null, `${i + 1}: ` + (p ? `${p.x}, ${p.y}` : "unset")));
  });
  box.append(mem);
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

function renderStats() {
  const t = $("stats");
  t.replaceChildren();
  if (!snap) return;
  const head = document.createElement("tr");
  for (const c of ["#", "Colony", "Robots", "Fleet value", "Parts"]) head.append(el("th", null, c));
  t.append(head);

  const rows = [...snap.colonies].sort((a, b) => b.fleet_value - a.fleet_value);
  rows.forEach((c, i) => {
    const tr = document.createElement("tr");
    tr.append(el("td", null, String(i + 1)));
    const name = el("td");
    const sw = el("span", "swatch");
    sw.style.background = colonyColor(c.colony);
    name.append(sw, document.createTextNode(colonyName(c.colony)));
    tr.append(name, el("td", null, String(c.robots)), el("td", null, String(c.fleet_value)),
      el("td", null, String(c.inventory)));
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
    box.append(el("p", "meta", "Idle."));
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
  draw();
  renderClock();
  renderInspector();
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
    conn("retry", `reconnecting in ${Math.round(backoff / 1000)}s…`);
    clearTimeout(retryTimer);
    retryTimer = setTimeout(connect, backoff);
    backoff = Math.min(backoff * 2, 15000);
    // EventSource never reports the status code, so an expired session is
    // indistinguishable from a dead server until something asks.
    const res = await fetch("/api/me").catch(() => null);
    if (res && res.status === 401) location.href = "/login";
  });
}

connect();
