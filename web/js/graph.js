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

const $ = (id) => document.getElementById(id);
const setText = (n, s) => { if (n.textContent !== s) n.textContent = s; };

export const SVGNS = "http://www.w3.org/2000/svg";
export const colonyVar = (id) => `--colony-${((id % 8) + 8) % 8}`;

// GRAPH_MAX bounds the series in the browser the way historyCap bounds it on
// the server: once past it, every second sample is dropped and the interval
// doubles. A page left open on a long match must not grow without limit.
const GRAPH_MAX = 512;

const METRICS = { score: "score", robots: "robots", collected: "parts collected" };

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

export function drawGraph() {
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
