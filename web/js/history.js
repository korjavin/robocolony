// The colony dashboard (design screen 1h): your robots and what happened to
// them, over one finished match or over every match you have played.
//
// It is still the history page. The nav is the shipped six items, /history is
// still where a finished match is listed and still the door into its replay —
// what grew is the middle: four numbers about *your* colony, the fight charts
// behind them, and the match list re-cut as the design's MATCH HISTORY table,
// which is the picker as well as the record.
//
// The graph is not reimplemented here — web/js/graph.js is the one copy of it,
// and this page feeds it the same lobby.History object the match view gets on
// its init frame; the axis, the gridlines and the legend are that module's
// optional markup, which this page carries and the match view does not. The
// door onwards is the Watch link, which is /match?id=N with replay=1: the same
// observer page, driven by the replay stream.
//
// Two things the design shows and this does not, both because the data does not
// exist yet rather than because they were dropped:
//
//   - PROGRAM PERFORMANCE (deposits and idle time per program version). Nothing
//     records per-program aggregates.
//   - the attacker's blueprint name in "what killed your robots", and the
//     "G-11 ARRIVES" annotations on the chart. The stored attribution is a
//     robot id and a colony (lobby.AttackerLosses), so that is what is shown —
//     inventing a name would be a lie with a tooltip.

import { colonyVar, seriesReset, mmss } from "./graph.js";

const $ = (id) => document.getElementById(id);
const err = (m) => { $("err").textContent = m || ""; };

function el(tag, className, text) {
  const e = document.createElement(tag);
  if (className) e.className = className;
  if (text !== undefined) e.textContent = text;
  return e;
}

async function api(path) {
  const res = await fetch(path, { headers: { Accept: "application/json" } });
  if (res.status === 401) { location.href = "/login"; return null; }
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

// mmss in graph.js formats ticks against the series' tick rate, which is only
// known once a match is selected. A list row has neither, only seconds.
const clock = (secs) => `${Math.floor(secs / 60)}:${String(secs % 60).padStart(2, "0")}`;
// The server sends RFC3339; the reader is in some other timezone than the box.
const when = (s) => (s ? new Date(s).toLocaleString() : "");
const day = (s) => (s ? new Date(s).toLocaleDateString(undefined,
  { month: "short", day: "numeric" }) : "");
const lengthOf = (m) => clock(Math.round(m.end_tick / (m.tick_rate || 10)));
const nameOf = (m) => (id) =>
  m.colonies.find((c) => c.id === id)?.display_name || `colony ${id}`;
const ordinal = (n) => {
  const s = ["th", "st", "nd", "rd"], v = n % 100;
  return n + (s[(v - 20) % 10] || s[v] || s[0]);
};

let matches = [];      // /api/history, newest first
let selected = null;   // match id
let detail = null;     // /api/history/{id} of the selected match
let me = null;         // /api/me, only to find which colony is yours
let range = "match";   // this match | 30d | all — what the four stats cover

// standing sorts a match's colonies the way the standing reads: best first.
const standing = (m) => [...m.colonies].sort((a, b) => b.score - a.score);
// A seat is yours when it is a human seat and the human is you. AI colonies
// carry user_id 0, and user ids start at 1, so no AI can ever match.
const seatOf = (m) => m.colonies.find((c) => c.user_id && c.user_id === me?.id);
const placeOf = (m, seat) => standing(m).findIndex((c) => c.id === seat.id) + 1;

// ---------------------------------------------------------------- the numbers

function stat(label, value, delta) {
  const d = el("div", "stat");
  d.append(el("div", "label", label), el("div", "v", String(value)),
    el("div", "delta", delta || ""));
  return d;
}

// The peak of a colony's robot count and when it happened — the design's
// "PEAK 13 AT 04:10". It is the series' business, not the standing's: the
// standing only knows the last tick.
function peakRobots(colonyID) {
  const s = detail?.history?.colonies?.find((c) => c.colony === colonyID);
  if (!s?.robots?.length) return null;
  let at = 0;
  s.robots.forEach((v, i) => { if (v > s.robots[at]) at = i; });
  return { peak: s.robots[at], tick: detail.history.ticks[at] };
}

const combatOf = (colonyID) =>
  detail?.combat?.colonies?.find((c) => c.colony === colonyID);

// The busiest bucket of a per-minute series, for a stat's delta line.
function busiest(per) {
  if (!per?.length) return null;
  let at = 0;
  per.forEach((v, i) => { if (v > per[at]) at = i; });
  return per[at] ? { n: per[at], minute: at + 1 } : null;
}

function thisMatchStats(m, seat) {
  const rows = standing(m);
  const place = placeOf(m, seat);
  const peak = peakRobots(seat.id);
  const c = combatOf(seat.id);
  const worst = c?.killed_by?.[0];
  const best = busiest(c?.kills_per);
  const lead = place > 1
    ? `LEAD ${rows[0].score - seat.score}`
    : (rows.length > 1 ? `AHEAD BY ${seat.score - rows[1].score}` : "UNCONTESTED");
  return [
    stat("Robots fielded", seat.robots,
      peak ? `PEAK ${peak.peak} AT ${mmss(peak.tick)}` : ""),
    stat("Losses", seat.losses,
      worst ? `${worst.losses} TO R-${worst.robot} · ${nameOf(m)(worst.colony)}` : ""),
    stat("Kills", seat.kills,
      best ? `${best.n} IN MINUTE ${String(best.minute).padStart(2, "0")}` : ""),
    stat("Score", seat.score, `${ordinal(place)} OF ${rows.length} · ${lead}`),
  ];
}

// Over a range it is the same four numbers summed over the matches you played,
// from the list rows alone: Status carries kills and losses since rc-pt6.10, so
// this costs no extra request. The peak-robots delta cannot be summed — it
// lives in a per-match series — so each tile says the most useful thing it can
// about the spread instead.
function rangeStats(mine) {
  const sum = (f) => mine.reduce((n, x) => n + f(x), 0);
  const max = (f) => mine.reduce((n, x) => Math.max(n, f(x)), 0);
  const wins = mine.filter((x) => placeOf(x.m, x.seat) === 1).length;
  return [
    stat("Robots fielded", sum((x) => x.seat.robots),
      `AT THE FINAL TICK OF ${mine.length} MATCH${mine.length === 1 ? "" : "ES"}`),
    stat("Losses", sum((x) => x.seat.losses),
      `WORST ${max((x) => x.seat.losses)} IN ONE MATCH`),
    stat("Kills", sum((x) => x.seat.kills),
      `BEST ${max((x) => x.seat.kills)} IN ONE MATCH`),
    stat("Score", sum((x) => x.seat.score), `WON ${wins} OF ${mine.length}`),
  ];
}

// played lists the matches in the current range that you actually had a seat
// in. Everything else on the page is about every match; these four numbers are
// about yours.
function played() {
  const since = range === "30d" ? Date.now() - 30 * 86400e3 : 0;
  return matches
    .filter((m) => Date.parse(m.finished_at) >= since)
    .map((m) => ({ m, seat: seatOf(m) }))
    .filter((x) => x.seat);
}

function renderStats() {
  const box = $("stats");
  const note = $("stats-note");
  box.replaceChildren();
  const scope = " The charts and the standing below are always the match you"
    + " picked.";
  if (range === "match") {
    const m = matches.find((x) => x.id === selected);
    const seat = m && seatOf(m);
    if (!m) { note.textContent = "No match has finished yet."; return; }
    if (!seat) {
      note.textContent = `You did not play ${m.name}, so there are no numbers of`
        + " yours in it. Its standing and its charts are below.";
      return;
    }
    box.append(...thisMatchStats(m, seat));
    note.textContent = `${m.name}, ${when(m.finished_at)}.`;
    return;
  }
  const mine = played();
  const over = range === "30d" ? "in the last 30 days" : "on record";
  if (mine.length === 0) {
    note.textContent = `You have not played a finished match ${over}.`;
    return;
  }
  box.append(...rangeStats(mine));
  note.textContent = `Your totals over ${mine.length} match`
    + `${mine.length === 1 ? "" : "es"} ${over}.` + scope;
}

// ----------------------------------------------------------------- the charts

// Kills and losses per minute, from the persisted per-bucket series. The bars
// stack in one column per bucket, so both are scaled against the tallest
// column; 80% of the column height is the ceiling, and the rest is the label
// under it.
function renderBars(m) {
  const box = $("bars");
  const note = $("bars-note");
  box.replaceChildren();
  const seat = seatOf(m);
  const c = seat && combatOf(seat.id);
  const bucket = detail?.combat?.bucket_ticks || 0;
  if (!c || !bucket || (!c.losses_per?.length && !c.kills_per?.length)) {
    note.textContent = seat
      ? "This match was recorded before the build that stores kills and losses,"
        + " so there is nothing to chart."
      : "You did not play this match.";
    return;
  }
  const kills = c.kills_per || [];
  const losses = c.losses_per || [];
  const n = Math.max(kills.length, losses.length);
  let top = 0;
  for (let i = 0; i < n; i++) top = Math.max(top, (kills[i] || 0) + (losses[i] || 0));
  for (let i = 0; i < n; i++) {
    const k = kills[i] || 0, l = losses[i] || 0;
    const col = el("div");
    col.title = `Minute ${i + 1}: ${k} kill${k === 1 ? "" : "s"}, `
      + `${l} loss${l === 1 ? "" : "es"}`;
    const bar = (cls, v) => {
      const d = el("div", cls);
      d.style.height = `${top ? (v / top) * 80 : 0}%`;
      return d;
    };
    col.append(bar("k", k), bar("l", l),
      el("div", "t", String(i + 1).padStart(2, "0")));
    box.append(col);
  }
  const dropped = c.losses - losses.reduce((a, b) => a + b, 0);
  note.textContent = `One bar per ${mmss(bucket)} of match time.`
    + (dropped > 0
      ? ` ${dropped} of ${c.losses} losses happened before the match's event feed`
        + " reached this far back, so they are not in the bars."
      : "");
}

// What killed your robots: the stored attribution, ranked. The label is the
// attacker's robot id and colony because that is all the record holds — a
// blueprint name would have to be invented.
function renderKilledBy(m) {
  const box = $("killedby");
  const note = $("killedby-note");
  box.replaceChildren();
  const seat = seatOf(m);
  const c = seat && combatOf(seat.id);
  if (!c) {
    note.textContent = seat
      ? "No attribution was stored for this match."
      : "You did not play this match.";
    return;
  }
  const ranked = c.killed_by || [];
  const top = ranked[0]?.losses || 1;
  for (const a of ranked) {
    const row = el("div", "meter");
    const bar = el("div", "bar");
    const fill = el("div");
    fill.style.width = `${(a.losses / top) * 100}%`;
    bar.append(fill);
    row.append(el("span", "k", `R-${a.robot} · ${nameOf(m)(a.colony)}`), bar,
      el("span", "v", String(a.losses)));
    box.append(row);
  }
  const attributed = ranked.reduce((n, a) => n + a.losses, 0);
  if (ranked.length === 0) {
    note.textContent = c.losses
      ? `${c.losses} robots lost, none of them to an attacker the event feed`
        + " still remembers."
      : "Nothing of yours was destroyed.";
    return;
  }
  note.textContent = "The attacker is a robot id and its colony: which blueprint"
    + " it was is not recorded yet."
    + (c.losses > attributed
      ? ` ${c.losses - attributed} of ${c.losses} losses have no attacker on record.`
      : "");
}

// ------------------------------------------------------------------ the lists

// The standing is the list row's colonies — lobby.Status plus the design §9
// score — with parts collected taken from the last sample of the series, which
// is the same number the graph's "parts collected" line ends on.
function renderStanding(m, d) {
  const t = $("standing");
  t.replaceChildren();
  const head = document.createElement("tr");
  for (const c of ["#", "Colony", "Score", "Robots", "Kills", "Losses", "Stock", "Parts"]) {
    head.append(el("th", null, c));
  }
  t.append(head);

  const rows = standing(m);
  rows.forEach((c, i) => {
    const series = d.history?.colonies?.find((x) => x.colony === c.id);
    const parts = series?.collected?.length ? series.collected[series.collected.length - 1] : 0;
    const tr = document.createElement("tr");
    // A draw has no winner: only mark the top row when nothing ties it.
    if (i === 0 && (rows.length === 1 || rows[1].score < c.score)) tr.className = "win";
    tr.append(el("td", null, String(i + 1)));
    const who = el("td", "who");
    const sw = el("span", "swatch");
    // var(), never a resolved colour — the same custom property the graph's
    // strokes and the match view's map read.
    sw.style.background = `var(${colonyVar(c.id)})`;
    who.append(sw, document.createTextNode(c.display_name + (c.ai ? ` (${c.ai})` : "")));
    if (tr.className === "win") who.append(el("span", "badge", "winner"));
    tr.append(who, el("td", "num", String(c.score)), el("td", "num", String(c.robots)),
      el("td", "num", String(c.kills || 0)), el("td", "num", String(c.losses || 0)),
      el("td", "num", String(c.inventory)), el("td", "num", String(parts)));
    t.append(tr);
  });
}

// The design's MATCH HISTORY table, which is also the picker: when, which
// lobby, and where you came — against /api/me, so "place" means your place.
function renderList() {
  const t = $("matches");
  t.replaceChildren();
  if (matches.length === 0) {
    const tr = document.createElement("tr");
    const td = el("td", "meta", "No match has finished yet. Start one from the"
      + " lobbies page — it lands here when it ends.");
    td.colSpan = 3;
    tr.append(td);
    t.append(tr);
    return;
  }
  const head = document.createElement("tr");
  for (const c of ["When", "Lobby", "Place"]) head.append(el("th", null, c));
  t.append(head);
  for (const m of matches) {
    const tr = document.createElement("tr");
    if (m.id === selected) tr.className = "on";
    const b = el("button", "pick", m.name);
    b.type = "button";
    if (m.id === selected) b.setAttribute("aria-current", "true");
    const lobby = el("td");
    lobby.append(b);
    const seat = seatOf(m);
    const place = seat ? `${placeOf(m, seat)} / ${m.colonies.length}` : "—";
    const cell = el("td", null, place);
    if (seat && placeOf(m, seat) === 1) cell.className = "first";
    const at = el("td", null, day(m.finished_at));
    at.title = `${when(m.finished_at)} · ${lengthOf(m)} long`;
    // One handler on the row: the whole row is the target for a pointer, and a
    // click on the button — which is what a keyboard and a screen reader reach,
    // and where the accessible name is — bubbles into it.
    tr.addEventListener("click", () => select(m));
    tr.append(at, lobby, cell);
    t.append(tr);
  }
}

// The door into replay — or the server's reason there is none. A match recorded
// by a build that no longer simulates the same way cannot be replayed, and a
// Watch button that only ever errors is worse than the sentence saying so.
function renderWatch(m, d) {
  const box = $("watch");
  const head = $("graph-open");
  box.replaceChildren();
  head.replaceChildren();
  if (d.replayable) {
    const a = el("a", "btn primary", `Watch ${m.name}`);
    a.href = `/match?id=${encodeURIComponent(m.id)}&replay=1`;
    box.append(a, el("p", "meta", "Plays back in the match view, with a scrubber,"
      + " pause and speed. Seeking rebuilds the world from the start, so it takes"
      + " a moment."));
    // The design opens the replay at the tick under the cursor. The match view
    // has no tick in its URL, so this opens at the start — the seek is the
    // scrubber's job until it does.
    const open = el("a", null, "Open the replay");
    open.href = a.href;
    head.append(open, document.createTextNode(" — from the start"));
    return;
  }
  const b = el("button", "btn", "Watch");
  b.type = "button";
  b.disabled = true;
  box.append(b, el("p", "meta", d.reason
    || "This match cannot be replayed by the build running now."));
}

async function select(m) {
  if (selected === m.id && detail) return;
  selected = m.id;
  detail = null;
  renderList();
  err("");
  $("detail-name").textContent = m.name;
  $("detail-when").textContent = `${when(m.started_at)} · ${lengthOf(m)} of match time`
    + ` · ${m.end_tick} ticks at ${m.tick_rate || 10}/s`;
  let d = null;
  try { d = await api(`/api/history/${encodeURIComponent(m.id)}`); }
  catch (e) { err(e.message); return; }
  if (!d || selected !== m.id) return; // a second click landed first
  detail = d;
  // Before anything reads mmss: the tick rate it formats against comes with the
  // series.
  seriesReset(d.history, d.info?.tick_rate || m.tick_rate, nameOf(m));
  renderStanding(m, d);
  renderWatch(m, d);
  renderBars(m);
  renderKilledBy(m);
  renderStats();
}

for (const b of $("range").querySelectorAll("button")) {
  b.addEventListener("click", () => {
    range = b.dataset.range;
    for (const o of $("range").querySelectorAll("button")) {
      o.setAttribute("aria-pressed", String(o === b));
    }
    renderStats();
  });
}

try {
  me = await api("/api/me");
  matches = (await api("/api/history")) || [];
  renderList();
  // The page is about one match at a time and the newest is the one just
  // finished; opening it saves the click that would otherwise be mandatory.
  if (matches.length) await select(matches[0]);
  else renderStats();
} catch (e) { err(e.message); }
