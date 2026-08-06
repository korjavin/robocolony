// Score over time (design §4.4), as one SVG polyline per colony. No charting
// library: this is a coordinate transform and a string, and a dependency here
// would buy axes nobody asked for.
//
// Two pages draw it — the live match view and the history page — from the same
// lobby.History object: {interval, ticks, colonies:[{colony, score, robots,
// collected}]}. On /match it rides the init frame already downsampled and grows
// from the tick stream, so nothing rides the 10 Hz frame and a reload or a late
// join still shows the whole match; on /history it arrives whole from
// /api/history/{id}. This module is where that code lives once — it was inside
// match.js, and the history page would have been the second copy of it.
//
// The markup is the contract, not a constructor argument: both pages carry
// #graph-metric, #graph-lines and #graph-note, and .graph in app.css styles
// them. The graph is built once and updated in place, and on the match page it
// lives in the Colonies panel rather than in #inspector, which is cleared on
// every tick.
//
// The colony dashboard (web/history.html) asks for three more: gridlines, the
// y axis they are labelled by, and a legend that names the colours. They are
// the *optional* half of the contract — the match view carries none of them and
// draws the same graph without — so they are read through opt() rather than
// $(). binding_test.go checks that every id this module looks up through $
// exists on every page that loads it, which is the right rule for the required
// half and would be the wrong one here.
//
// The y labels are HTML, not <text>: the svg is preserveAspectRatio="none", so
// anything drawn inside it is stretched horizontally by whatever width the page
// gives it, and stretched digits are unreadable. Lines survive that; glyphs do
// not.

const $ = (id) => document.getElementById(id);
const opt = $; // the same lookup, a different contract: may be absent
const setText = (n, s) => { if (n.textContent !== s) n.textContent = s; };

export const SVGNS = "http://www.w3.org/2000/svg";
export const colonyVar = (id) => `--colony-${((id % 8) + 8) % 8}`;

// GRAPH_MAX bounds the series in the browser the way historyCap bounds it on
// the server: once past it, every second sample is dropped and the interval
// doubles. A page left open on a long match must not grow without limit.
const GRAPH_MAX = 512;

const METRICS = { score: "score", robots: "robots", collected: "parts collected" };

// The drawing box, in viewBox units. The dashboard's y-axis column lines its
// labels up against these, so pad is padding on the *value* axis: the top
// gridline sits at pad from the top edge and the zero line at pad from the
// bottom. web/history.html repeats the ratio in one CSS declaration and says so.
const W = 600, H = 180, PAD = 6;

let series = null;   // {interval, ticks, colonies:[{colony, score, robots, collected}]}
let rate = 10;       // ticks per second, for mmss
let colonyName = (id) => `colony ${id}`;

// seriesReset adopts a whole series. The tick rate and the colony-name lookup
// come with it because they belong to the same match: the match page has them
// on the init frame, the history page in /api/history/{id}.
export function seriesReset(h, tickRate, name) {
  if (tickRate) rate = tickRate;
  if (name) colonyName = name;
  series = h && Array.isArray(h.ticks) && h.interval > 0 ? h : null;
  drawGraph();
}

// seriesAppend adds this snapshot to the series if it lands on a sampling tick
// and is newer than what is there. Both guards matter: the init frame can be
// one sample ahead of the first tick frame (the server takes them under
// separate locks), and a reconnect replays a tick already recorded.
export function seriesAppend(s) {
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

export const mmss = (ticks) => {
  const s = Math.round(ticks / (rate || 10));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
};

// The y axis: four intervals of one step, drawn as gridlines in the svg and
// labelled in HTML beside it. Both are optional (see the header), and both are
// cleared rather than left stale when the series goes away.
function drawAxis(top, step, y) {
  const grid = opt("graph-grid"), labels = opt("graph-yaxis");
  if (!grid && !labels) return;
  const values = top ? [4, 3, 2, 1, 0].map((n) => n * step) : [];
  if (grid) {
    grid.replaceChildren(...values.map((v) => {
      const l = document.createElementNS(SVGNS, "line");
      l.setAttribute("x1", 0);
      l.setAttribute("x2", W);
      l.setAttribute("y1", y(v).toFixed(1));
      l.setAttribute("y2", y(v).toFixed(1));
      return l;
    }));
  }
  if (labels) {
    labels.replaceChildren(...values.map((v) => {
      const d = document.createElement("div");
      d.textContent = String(v);
      return d;
    }));
  }
}

// The legend: which colour is whose. The graph already carries a <title> per
// polyline, which is a tooltip nobody hovers — on a dashboard the mapping is
// the first thing read, so it is on screen.
function drawLegend() {
  const box = opt("graph-legend");
  if (!box) return;
  box.replaceChildren(...(series?.colonies || []).map((c) => {
    const s = document.createElement("span");
    const sw = document.createElement("span");
    sw.className = "swatch";
    sw.style.background = `var(${colonyVar(c.colony)})`;
    s.append(sw, document.createTextNode(colonyName(c.colony)));
    return s;
  }));
}

export function drawGraph() {
  const lines = $("graph-lines");
  const note = $("graph-note");
  const metric = $("graph-metric").value;
  if (!series || series.ticks.length < 2) {
    lines.replaceChildren();
    drawAxis(0, 0, () => 0);
    drawLegend();
    setText(note, "Waiting for the first samples — one every "
      + `${mmss(series?.interval || 100)} of match time.`);
    return;
  }

  const t0 = series.ticks[0];
  const span = Math.max(1, series.ticks[series.ticks.length - 1] - t0);
  let peak = 0;
  for (const c of series.colonies) for (const v of c[metric]) peak = Math.max(peak, v);
  // Four intervals, and the top gridline is the peak itself: every series here
  // counts things, so an integer step keeps the labels whole and the axis tight
  // — a "round number" rule would put 512 robots on an axis to 800.
  const step = Math.max(1, Math.ceil(peak / 4));
  const top = step * 4;
  const y = (v) => H - PAD - (v / top) * (H - 2 * PAD);
  drawAxis(top, step, y);
  drawLegend();

  lines.replaceChildren(...series.colonies.map((c) => {
    const p = document.createElementNS(SVGNS, "polyline");
    p.setAttribute("points", series.ticks.map((t, i) =>
      `${((t - t0) / span * W).toFixed(1)},`
      + `${y(c[metric][i] || 0).toFixed(1)}`).join(" "));
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
