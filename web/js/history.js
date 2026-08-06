// Finished matches: the list, one match's final standing, and its
// score-over-time graph.
//
// The graph is not reimplemented here — web/js/graph.js is the one copy of it,
// and this page feeds it the same lobby.History object the match view gets on
// its init frame. The door onwards is the Watch link, which is /match?id=N with
// replay=1: the same observer page, driven by the replay stream.

import { colonyVar, seriesReset } from "./graph.js";

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
const lengthOf = (m) => clock(Math.round(m.end_tick / (m.tick_rate || 10)));
const nameOf = (m) => (id) =>
  m.colonies.find((c) => c.id === id)?.display_name || `colony ${id}`;

let matches = [];      // /api/history, newest first
let selected = null;   // match id

function renderList() {
  const ul = $("matches");
  ul.replaceChildren();
  if (matches.length === 0) {
    ul.append(el("li", "meta", "No match has finished yet. Start one from the"
      + " lobbies page — it lands here when it ends."));
    return;
  }
  for (const m of matches) {
    const b = el("button", "pick");
    b.type = "button";
    b.setAttribute("aria-pressed", String(m.id === selected));
    b.append(el("div", "who", m.name),
      el("div", "meta", `${when(m.finished_at)} · ${lengthOf(m)} long`
        + ` · ${m.colonies.length} colonies`));
    b.addEventListener("click", () => select(m));
    const li = document.createElement("li");
    li.append(b);
    ul.append(li);
  }
}

// The standing is the list row's colonies — lobby.Status plus the design §9
// score — with parts collected taken from the last sample of the series, which
// is the same number the graph's "parts collected" line ends on.
function renderStanding(m, d) {
  const t = $("standing");
  t.replaceChildren();
  const head = document.createElement("tr");
  for (const c of ["#", "Colony", "Score", "Robots", "Stock", "Parts"]) {
    head.append(el("th", null, c));
  }
  t.append(head);

  const rows = [...m.colonies].sort((a, b) => b.score - a.score);
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
      el("td", "num", String(c.inventory)), el("td", "num", String(parts)));
    t.append(tr);
  });
}

// The door into replay — or the server's reason there is none. A match recorded
// by a build that no longer simulates the same way cannot be replayed, and a
// Watch button that only ever errors is worse than the sentence saying so.
function renderWatch(m, d) {
  const box = $("watch");
  box.replaceChildren();
  if (d.replayable) {
    const a = el("a", "btn primary", `Watch ${m.name}`);
    a.href = `/match?id=${encodeURIComponent(m.id)}&replay=1`;
    box.append(a, el("p", "meta", "Plays back in the match view, with a scrubber,"
      + " pause and speed. Seeking rebuilds the world from the start, so it takes"
      + " a moment."));
    return;
  }
  const b = el("button", "btn", "Watch");
  b.type = "button";
  b.disabled = true;
  box.append(b, el("p", "meta", d.reason
    || "This match cannot be replayed by the build running now."));
}

async function select(m) {
  selected = m.id;
  renderList();
  err("");
  $("detail-name").textContent = m.name;
  $("detail-when").textContent = `${when(m.started_at)} · ${lengthOf(m)} of match time`
    + ` · ${m.end_tick} ticks at ${m.tick_rate || 10}/s`;
  let d = null;
  try { d = await api(`/api/history/${encodeURIComponent(m.id)}`); }
  catch (e) { err(e.message); return; }
  if (!d || selected !== m.id) return; // a second click landed first
  renderStanding(m, d);
  renderWatch(m, d);
  seriesReset(d.history, d.info?.tick_rate || m.tick_rate, nameOf(m));
}

try {
  matches = (await api("/api/history")) || [];
  renderList();
  // The page is about one match at a time and the newest is the one just
  // finished; opening it saves the click that would otherwise be mandatory.
  if (matches.length) await select(matches[0]);
} catch (e) { err(e.message); }
