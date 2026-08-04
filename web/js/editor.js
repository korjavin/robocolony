// The program and blueprint editor. Plain ES module, no build step: the
// catalogue, the limits, the templates, the §6.3 constraints and every balance
// number come from the server, so this file never holds a second copy of the
// language or of the simulation's tables.
const $ = (id) => document.getElementById(id);
function el(tag, props = {}, ...kids) {
  const n = document.createElement(tag);
  Object.assign(n, props);
  n.append(...kids.filter((k) => k));
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
  if (!res.ok) {
    const e = new Error(data.error || res.statusText);
    e.status = res.status;
    e.data = data;
    throw e;
  }
  return data;
}

// ---------------------------------------------------------------------------
// State. The catalogue, the limits and the templates all come from the server
// (/api/language) so this file never holds a second copy of the language.
// ---------------------------------------------------------------------------
let lang = null;
let preds = new Map();      // id -> spec
let acts = new Map();
let programs = [];
let blueprints = [];
let current = null;         // { id, name, program }
let findings = { errors: [], warnings: [], notes: [] };
let pending = 0;
let dryrun = null;          // last /api/programs/dryrun report, or null

const blank = () => ({
  id: 0, name: "new program",
  program: { v: lang.schema_version, name: "new program", rules: [] },
});
const firstPred = () => ({ op: "pred", pred: lang.catalogue.predicates[0].id });
const newRule = () => ({ when: firstPred(), then: [{ do: lang.catalogue.actions[0].id }] });
const blueprintID = () => Number($("blueprint").value || 0);

// ---------------------------------------------------------------------------
// Widgets
// ---------------------------------------------------------------------------

// catSelect builds a grouped dropdown straight from a catalogue slice. An id
// the catalogue no longer knows still shows up, so an old program is editable
// rather than silently rewritten.
function catSelect(specs, value, onChange) {
  const sel = el("select");
  const groups = new Map();
  for (const s of specs) {
    let og = groups.get(s.group);
    if (!og) { og = el("optgroup", { label: s.group }); groups.set(s.group, og); sel.append(og); }
    og.append(el("option", { value: s.id, textContent: s.label }));
  }
  sel.value = value ?? "";
  if (sel.value !== (value ?? "")) {
    sel.prepend(el("option", { value: value ?? "", textContent: `${value} (unknown)` }));
    sel.value = value ?? "";
  }
  sel.addEventListener("change", () => onChange(sel.value));
  return sel;
}

// argInput renders the parameter a predicate or action takes, or nothing.
function argInput(spec, value, onChange) {
  if (!spec || spec.arg === "none") return null;
  const point = spec.arg === "point";
  const inp = el("input", {
    type: "number",
    min: point ? 1 : 0,
    max: point ? lang.mem_points : 100,
    value: value ?? (point ? 1 : 0),
    title: point ? `memory point 1..${lang.mem_points}` : "percent 0..100",
  });
  inp.addEventListener("change", () => onChange(Number(inp.value)));
  return inp;
}

function iconButton(label, title, onClick, disabled = false) {
  const b = el("button", { textContent: label, title, disabled });
  b.addEventListener("click", onClick);
  return b;
}

const badge = (text, title) => el("span", { className: "badge", textContent: text, title: title || "" });

// ---------------------------------------------------------------------------
// Condition tree
// ---------------------------------------------------------------------------

// renderCond draws one node. replace() swaps this node in its parent, remove()
// drops it; the root supplies its own versions so a rule's whole condition can
// be wrapped or unwrapped like any other node.
function renderCond(node, replace, remove) {
  if (node.op === "and" || node.op === "or") return renderGroup(node, replace, remove);
  if (node.op === "not") return renderNot(node, replace, remove);

  const spec = preds.get(node.pred);
  const row = el("div", { className: "row" });
  row.append(catSelect(lang.catalogue.predicates, node.pred, (v) => {
    node.pred = v;
    const s = preds.get(v);
    if (!s || s.arg === "none") delete node.arg; else node.arg = node.arg || (s.arg === "point" ? 1 : 0);
    changed();
  }));
  const arg = argInput(spec, node.arg, (v) => { node.arg = v; changed(); });
  if (arg) row.append(arg);
  if (spec && spec.needs && spec.needs.length) {
    row.append(badge(`needs ${spec.needs.join(" + ")}`));
  }
  row.append(...wrapButtons(node, replace), notButton(node, replace));
  if (remove) row.append(iconButton("✕", "remove this condition", remove));
  if (spec && spec.desc) row.append(el("p", { className: "help", textContent: spec.desc }));
  return row;
}

// wrapButtons put a node inside a new parent. They sit on the predicate row and
// on the NOT header, so both "NOT (A AND B)" and "(NOT A) AND B" are reachable
// whichever end the player starts from — and, because a NOT draws as its own
// labelled box, the two are visibly different pictures rather than a precedence
// rule to remember (design §10.10).
const wrapButtons = (node, replace) => [
  iconButton("AND", "wrap this condition in an ALL group", () => replace({ op: "and", of: [node, firstPred()] })),
  iconButton("OR", "wrap this condition in an ANY group", () => replace({ op: "or", of: [node, firstPred()] })),
];

const notButton = (node, replace) =>
  iconButton("NOT", "invert this condition: true exactly when it is false",
    () => replace({ op: "not", of: [node] }));

// renderNot draws the negation as a box of its own around its single operand.
// The operand has no ✕ — a NOT holds exactly one condition, and emptying it
// would produce a node the server refuses. "unwrap" is how the NOT goes away.
function renderNot(node, replace, remove) {
  const box = el("div", { className: "group not" });
  const head = el("div", { className: "row" },
    el("span", { className: "glabel", textContent: "NOT — true when the condition inside is false" }),
    // These wrap the NOT itself, giving "(NOT A) AND B". Pressing AND on the
    // condition *inside* the box gives "NOT (A AND B)" instead — two different
    // boxes on screen, which is the whole point of drawing it this way.
    ...wrapButtons(node, replace),
    iconButton("unwrap", "drop the NOT and keep the condition inside",
      () => replace(node.of[0] || firstPred())));
  if (remove) head.append(iconButton("✕", "remove this condition", remove));
  const kids = el("div", { className: "kids" },
    renderCond(node.of[0] ?? firstPred(), (next) => { node.of = [next]; changed(); }, null));
  box.append(head, kids);
  return box;
}

function renderGroup(node, replace, remove) {
  const box = el("div", { className: "group" });
  const head = el("div", { className: "row" });
  const op = el("select");
  op.append(el("option", { value: "and", textContent: "ALL of" }),
            el("option", { value: "or", textContent: "ANY of" }));
  op.value = node.op;
  op.addEventListener("change", () => { node.op = op.value; changed(); });
  head.append(el("span", { className: "glabel", textContent: "match" }), op,
    iconButton("+ condition", "add a condition to this group", () => { node.of.push(firstPred()); changed(); }),
    iconButton("+ group", "add a nested group", () => { node.of.push({ op: "and", of: [firstPred()] }); changed(); }),
    iconButton("unwrap", "replace this group with its first condition",
      () => replace(node.of[0] || firstPred())),
    notButton(node, replace));
  if (remove) head.append(iconButton("✕", "remove this group", remove));
  const kids = el("div", { className: "kids" });
  node.of.forEach((kid, i) => {
    kids.append(renderCond(kid,
      (next) => { node.of[i] = next; changed(); },
      () => { node.of.splice(i, 1); changed(); }));
  });
  box.append(head, kids);
  return box;
}

// ---------------------------------------------------------------------------
// Rules
// ---------------------------------------------------------------------------

function renderRules() {
  const host = $("rules");
  host.replaceChildren();
  const rules = current.program.rules;
  if (rules.length === 0) {
    host.append(el("p", { className: "meta", textContent: "No rules yet. Add one, or start from a template." }));
  }
  rules.forEach((rule, i) => host.append(renderRule(rule, i, rules.length)));
  $("addrule").disabled = rules.length >= lang.limits.max_rules;
}

function renderRule(rule, i, total) {
  const card = el("div", { className: "rule" });
  const head = el("header");
  head.append(
    dragHandle(card, i),
    el("span", { className: "prio", textContent: String(i + 1), title: `priority ${i + 1}` }),
    iconButton("▲", "raise priority", () => move(i, -1), i === 0),
    iconButton("▼", "lower priority", () => move(i, 1), i === total - 1),
    iconButton("✕", "delete this rule", () => { current.program.rules.splice(i, 1); changed(); }),
  );
  for (const b of dryRunBadges(i)) head.append(b);
  const when = el("div", { className: "when" },
    renderCond(rule.when, (next) => { rule.when = next; changed(); }, null));
  const then = el("div", { className: "then" });
  rule.then.forEach((action, j) => then.append(renderAction(rule, action, j)));
  then.append(iconButton("+ action", "add an action to this rule",
    () => { rule.then.push({ do: lang.catalogue.actions[0].id }); changed(); },
    rule.then.length >= lang.limits.max_actions_per_rule));
  card.append(head, el("div", { className: "kw", textContent: "WHEN" }), when,
    el("div", { className: "kw", textContent: "THEN" }), then);

  for (const is of issuesFor(i)) card.append(issueLine(is));
  makeDropTarget(card, i);
  return card;
}

function renderAction(rule, action, j) {
  const spec = acts.get(action.do);
  const row = el("div", { className: "row" });
  row.append(catSelect(lang.catalogue.actions, action.do, (v) => {
    action.do = v;
    const s = acts.get(v);
    if (!s || s.arg === "none") delete action.arg; else action.arg = action.arg || (s.arg === "point" ? 1 : 0);
    changed();
  }));
  const arg = argInput(spec, action.arg, (v) => { action.arg = v; changed(); });
  if (arg) row.append(arg);
  if (spec) {
    row.append(badge(
      spec.primary ? "ends the tick" : "side effect, keeps checking",
      spec.primary
        ? "a primary action finishes the rule list for this tick"
        : "memory writes and broadcasts run and evaluation continues down the list",
    ));
  }
  if (spec && spec.needs && spec.needs.length) {
    row.append(badge(`needs ${spec.needs.join(" + ")}`));
  }
  row.append(iconButton("✕", "remove this action", () => { rule.then.splice(j, 1); changed(); }));
  if (spec && spec.desc) row.append(el("p", { className: "help", textContent: spec.desc }));
  return row;
}

function move(i, delta) {
  const rules = current.program.rules;
  const j = i + delta;
  if (j < 0 || j >= rules.length) return;
  [rules[i], rules[j]] = [rules[j], rules[i]];
  changed();
}

// ---------------------------------------------------------------------------
// Drag-and-drop reordering (design §10.10). Native HTML5 DnD, no dependency.
// The ▲▼ buttons stay: they are the keyboard path, and dragging is not.
//
// Only the handle is draggable, so a number field inside a card can still be
// selected with the mouse. The drag image is the whole card, because the card
// is what is moving.
// ---------------------------------------------------------------------------
let dragFrom = null;

function dragHandle(card, i) {
  const h = el("span", {
    className: "handle", textContent: "⠿", draggable: true,
    title: "drag to reorder — or use ▲ ▼, which the keyboard can reach",
  });
  h.addEventListener("dragstart", (ev) => {
    dragFrom = i;
    card.classList.add("dragging");
    // Firefox refuses to start a drag without payload, even unused payload.
    ev.dataTransfer.setData("text/plain", String(i));
    ev.dataTransfer.effectAllowed = "move";
    ev.dataTransfer.setDragImage(card, 12, 12);
  });
  h.addEventListener("dragend", () => { dragFrom = null; clearDropMarks(); });
  return h;
}

// makeDropTarget marks the card as a drop site. Which half of the card the
// pointer is in decides whether the rule lands above or below it — rule order
// is the program, so the landing slot is drawn, never guessed.
function makeDropTarget(card, i) {
  const below = (ev) => ev.clientY > card.getBoundingClientRect().top + card.offsetHeight / 2;
  card.addEventListener("dragover", (ev) => {
    if (dragFrom === null) return;
    ev.preventDefault();
    ev.dataTransfer.dropEffect = "move";
    card.classList.toggle("over-below", below(ev));
    card.classList.toggle("over-above", !below(ev));
  });
  card.addEventListener("dragleave", () => card.classList.remove("over-above", "over-below"));
  card.addEventListener("drop", (ev) => {
    if (dragFrom === null) return;
    ev.preventDefault();
    reorder(dragFrom, i + (below(ev) ? 1 : 0));
  });
}

function clearDropMarks() {
  for (const n of document.querySelectorAll(".dragging, .over-above, .over-below")) {
    n.classList.remove("dragging", "over-above", "over-below");
  }
}

// reorder moves the rule at `from` into the gap before index `to`, where `to`
// is counted in the list as it stands now — so dropping below card 4 is to = 5
// whether the rule came from above or below it.
function reorder(from, to) {
  const rules = current.program.rules;
  if (from === to || from + 1 === to) { clearDropMarks(); return; }
  const [r] = rules.splice(from, 1);
  rules.splice(from < to ? to - 1 : to, 0, r);
  changed();
}

// ---------------------------------------------------------------------------
// Validation feedback. Errors block the save, warnings never do (design §10.10).
// ---------------------------------------------------------------------------

// Buckets in descending loudness: an error, then a warning, then a note. Notes
// are observations about a correct program, so they are rendered quietly and
// never counted anywhere that gates a save.
const issuesFor = (rule) =>
  [...findings.errors, ...findings.warnings, ...(findings.notes || [])].filter((e) => e.rule === rule);

function issueLine(is) {
  return el("div", { className: `issue ${is.severity}`, textContent: `${is.severity}: ${is.message}` });
}

function renderIssues() {
  const host = $("issues");
  host.replaceChildren();
  for (const is of issuesFor(-1)) host.append(issueLine(is));
  const n = findings.errors.length;
  $("save").disabled = n > 0;
  $("dup").disabled = n > 0;
  $("save").title = n > 0 ? "fix the errors first; warnings do not block saving" : "";
}

let timer = null;
function changed() {
  // Any edit invalidates the last dry run: it was measured on a program that no
  // longer exists, and after a reorder its rule numbers point at other rules.
  // The same goes for whatever the last refusal said about it.
  dryrun = null;
  err("");
  render();
  clearTimeout(timer);
  timer = setTimeout(validate, 200);
}

async function validate() {
  const seq = ++pending;
  try {
    const res = await api("POST", "/api/programs/validate", {
      program: current.program, blueprint_id: blueprintID(),
    });
    if (!res || seq !== pending) return;
    findings = res;
    renderRules();
    renderIssues();
  } catch (e) { err(e.message); }
}

// ---------------------------------------------------------------------------
// Dry run: "does this program do anything at all?" (POST /api/programs/dryrun)
//
// Explicit button only — the endpoint runs a real simulation server-side, and
// firing it on a keystroke would be a denial of service with extra steps.
// ---------------------------------------------------------------------------

// dryRunBadges is what turns the report into an edit: a rule that never fired is
// called out on its own card, not buried in a summary. never_fired is top-level
// and exhaustive — the trace records every rule that matched, not only the one
// that took the tick — so a side-effect-only rule that ran counts as fired.
function dryRunBadges(i) {
  if (!dryrun) return [];
  const out = [];
  if (dryrun.never_fired.includes(i)) {
    out.push(el("span", {
      className: "badge never", textContent: "never fired",
      title: "in the dry run this rule never matched — an earlier rule took every tick it wanted, or its condition never held",
    }));
  }
  const row = (dryrun.rules || []).find((r) => r.rule === i);
  if (row && row.fired > 0) {
    out.push(badge(`fired ${row.fired}×`, `first at tick ${row.first_tick}`));
  }
  return out;
}

const when = (ev) => (ev.count > 0 ? `${ev.count}×, first at tick ${ev.first_tick}` : "never");

function renderDryRun() {
  const host = $("dryrun");
  host.replaceChildren();
  if (!dryrun) return;
  const d = dryrun;
  const lines = [
    d.acted.count > 0
      ? `Acted on ${d.acted.count} of ${d.decisions} decisions, first at tick ${d.acted.first_tick}.`
      : `Never acted: in ${d.decisions} decisions no rule ever took a tick.`,
    d.never_fired.length > 0
      ? `Rules that never fired: ${d.never_fired.map((i) => i + 1).join(", ")} — marked on the cards.`
      : "Every rule that can be seen firing did fire.",
    `Picked something up: ${when(d.picked_up)}.`,
    `Delivered to base: ${when(d.deposited)}.`,
  ];
  // Combat. The attack line is only printed when the program actually took a
  // shot: a scavenger design carries no weapon, and "never attacked" on a
  // blueprint that cannot attack reads as a failure it is not. The damage line
  // is unconditional, because it is the proof that there was an opponent — a
  // report with no combat in it at all could otherwise be mistaken for the
  // empty arena this replaced.
  if (d.attacked.count > 0) {
    lines.push(`Attacked: ${when(d.attacked)} — ${d.hit.count} of those took health off it,`
      + ` ${d.damage_dealt} damage in total${d.kills > 0 ? `, destroying ${d.kills}` : ""}.`);
  }
  lines.push(d.survived
    ? `Took ${d.damage_taken} damage from the sparring partner over ${d.took_damage.count} ticks;`
      + ` ${d.health} of ${d.max_health} health left.`
    : `Destroyed at tick ${d.destroyed_tick} after ${d.damage_taken} damage — nothing`
      + " after that tick is your program.");
  if (d.idle.count > 0) {
    lines.push(`Idle on ${d.idle.count} decisions${d.idle_reason ? ` — last reason: ${d.idle_reason}` : ""}.`);
  }
  const box = el("div", { className: "report" },
    el("strong", { textContent: "Dry run" }));
  for (const t of lines) box.append(el("div", { textContent: t }));
  box.append(el("p", {
    className: "meta",
    textContent: `${d.ticks} ticks on a ${d.width}×${d.height} practice arena, seed ${d.seed}. `
      + "You get one unarmed scout that calls out enemies it sees, against one hostile "
      + "sparring partner that hunts you on radar — so combat, defensive and signal rules "
      + "all have something to match. "
      + "Two runs of the same program are always comparable; this is a smoke test, not a match.",
  }));
  host.append(box);
}

async function tryIt() {
  err(""); status("");
  $("tryit").disabled = true;
  try {
    const res = await api("POST", "/api/programs/dryrun", {
      program: current.program, blueprint_id: blueprintID(),
    });
    if (!res) return;
    dryrun = res;
    renderRules();
    renderDryRun();
  } catch (e) {
    dryrun = null;
    renderDryRun();
    // 422 carries validation findings and is shown like a refused save. 429 is
    // the one-run-per-second limit; the server's own wording says to wait, and
    // retrying it silently is exactly what the limit exists to stop.
    if (e.status === 422) showRefusal(e); else err(e.message);
  } finally {
    $("tryit").disabled = false;
  }
}

// ---------------------------------------------------------------------------
// Library
// ---------------------------------------------------------------------------

function renderLibrary() {
  const host = $("library");
  host.replaceChildren();
  if (programs.length === 0) {
    // Reachable only if a read ever comes back empty: the library seeds the
    // three worked programs whenever it has none, so a player cannot strand
    // themselves by deleting everything. This says what to do about it anyway,
    // because a robot at its base can only be given a program from here.
    host.append(el("li", {
      className: "meta",
      textContent: "Empty. Start from a template, import a file, or add rules and Save.",
    }));
  }
  for (const p of programs) {
    const li = el("li", { className: p.id === current.id ? "on" : "" });
    li.append(el("div", { textContent: p.name }),
      el("div", { className: "meta", textContent: `${p.program.rules.length} rules · ${p.updated_at.slice(0, 10)}` }));
    li.addEventListener("click", () => open(p));
    host.append(li);
  }
}

function open(p) {
  current = { id: p.id, name: p.name, program: structuredClone(p.program) };
  $("name").value = p.name;
  findings = { errors: [], warnings: [], notes: [] };
  status("");
  changed();
}

function render() {
  renderLibrary();
  renderRules();
  renderIssues();
  renderDryRun();
}

async function reloadLibrary() {
  const data = await api("GET", "/api/programs");
  if (!data) return;
  programs = data.programs;
  renderLibrary();
}

// ---------------------------------------------------------------------------
// Blueprints: design the robot, not just the program.
//
// Every number shown here — mass, value, derived health, effective speed — and
// the §6.3 verdict itself come from the server. sim.Blueprint.Validate is
// authoritative and the balance tables are placeholders (E7.3 retunes them), so
// neither is copied into this file.
// ---------------------------------------------------------------------------

function renderBlueprints() {
  const sel = $("blueprint");
  const keep = sel.value;
  sel.replaceChildren();
  for (const b of blueprints) sel.append(el("option", { value: String(b.id), textContent: b.name }));
  if (blueprints.some((b) => String(b.id) === keep)) sel.value = keep;
  renderBlueprintMeta();
}

function renderBlueprintMeta() {
  const b = blueprints.find((x) => x.id === blueprintID());
  $("bpmeta").replaceChildren();
  if (!b) return;
  $("bpmeta").append(
    el("div", { textContent: b.components.map(componentName).join(", ") }),
    statLine(b),
  );
}

// statLine is the trade-off in one row: what the parts weigh, what they are
// worth to the fleet score, how much damage the body soaks and how fast the
// mass lets it move.
function statLine(s) {
  const row = el("div", { className: "stats" });
  const cell = (label, value, title) =>
    el("span", { className: "stat", title },
      el("b", { textContent: String(value) }), el("span", { textContent: label }));
  row.append(
    cell("health", s.health, "derived from the armored body (design §6.1)"),
    cell("speed", s.speed, "effective speed: locomotion base speed minus the mass penalty (design §6.4)"),
    cell("mass", s.mass, "total component mass — this is what the speed model taxes"),
    cell("value", s.value, "total component value, which is what the fleet score counts (design §9)"),
  );
  return row;
}

const componentName = (v) => (lang.components.find((c) => c.variant === v) || { name: `#${v}` }).name;

let picked = [];
let bpStats = null;     // last /api/blueprints/preview verdict
let bpStatsFor = "";    // the parts list that verdict describes
let bpTimer = null;
const pickedKey = () => picked.join(",");

// renderParts draws the palette grouped by the catalogue's own kind field, so
// E7.2 adding legs or an enemy-robot radar needs no change here.
function renderParts() {
  const host = $("bpparts");
  host.replaceChildren();
  const kinds = new Map();
  for (const c of lang.components) {
    if (!kinds.has(c.kind)) kinds.set(c.kind, []);
    kinds.get(c.kind).push(c);
  }
  for (const [kind, comps] of kinds) {
    const box = el("div", { className: "kindrow" }, el("span", { className: "kindname", textContent: kind }));
    for (const c of comps) {
      box.append(iconButton(`+ ${c.name}`, `mass ${c.mass} · value ${c.value}`, () => {
        picked.push(c.variant);
        previewBlueprint();
      }));
    }
    host.append(box);
  }
}

function renderPicked() {
  const host = $("bppicked");
  host.replaceChildren();
  if (picked.length === 0) {
    host.append(el("span", { className: "meta", textContent: "Nothing installed yet." }));
  }
  picked.forEach((v, i) => {
    const chip = el("span", { className: "chip" }, el("span", { textContent: componentName(v) }));
    chip.append(iconButton("✕", "remove", () => { picked.splice(i, 1); previewBlueprint(); }));
    host.append(chip);
  });
}

function renderBlueprintDraft() {
  renderPicked();
  const host = $("bpstats");
  host.replaceChildren();
  if (bpStats) {
    host.append(statLine(bpStats));
    if (!bpStats.ok) host.append(el("div", { className: "issue error", textContent: bpStats.error }));
  }
  // The server's verdict is the gate, exactly as it is for the save itself —
  // and only while it still describes the parts list on screen. A verdict for
  // some earlier design must never open the button for this one.
  $("bpcreate").disabled = !(bpStats && bpStats.ok && bpStatsFor === pickedKey());
}

// previewBlueprint asks the server what the current parts list would build.
// Debounced, because clicking through the palette is a burst of edits.
function previewBlueprint() {
  renderBlueprintDraft();
  clearTimeout(bpTimer);
  bpTimer = setTimeout(async () => {
    const key = pickedKey();
    try {
      const res = await api("POST", "/api/blueprints/preview", { components: picked });
      // Responses can land out of order; one for a design the player has
      // already moved on from is dropped rather than shown.
      if (!res || key !== pickedKey()) return;
      bpStats = res;
      bpStatsFor = key;
      renderBlueprintDraft();
    } catch (e) { err(e.message); }
  }, 150);
}

// ---------------------------------------------------------------------------
// Actions on the whole program
// ---------------------------------------------------------------------------

// showRefusal renders a save the server refused. Its errors and warnings arrive
// in their own lists and stay that way.
function showRefusal(e) {
  err(e.message);
  if (e.data && e.data.errors) {
    findings = { errors: e.data.errors, warnings: e.data.warnings || [], notes: e.data.notes || [] };
    renderRules();
    renderIssues();
  }
}

async function save(asCopy) {
  err(""); status("");
  const name = asCopy ? `${$("name").value} copy` : $("name").value;
  const body = { name, program: { ...current.program, name }, blueprint_id: blueprintID() };
  const create = asCopy || current.id === 0;
  try {
    const saved = await api(create ? "POST" : "PUT", create ? "/api/programs" : `/api/programs/${current.id}`, body);
    if (!saved) return;
    current = { id: saved.id, name: saved.name, program: structuredClone(saved.program) };
    $("name").value = saved.name;
    status("saved");
    await reloadLibrary();
    render();
  } catch (e) { showRefusal(e); }
}

// ---------------------------------------------------------------------------
// Export and import.
//
// The file *is* the wire format — internal/prog's {"v":1,"name":...,"rules":[]}
// — with no wrapper and no extra fields, so what is downloaded here is what the
// server stores and what prog.Decode reads back. There is no server round-trip
// on export: the editor already holds the program.
//
// No blueprint travels with the file. A blueprint is not part of the program
// document (§10.10), and a blueprint id means nothing in another player's
// library, so an import is instead checked against the blueprint selected in
// this editor — server-side, on /api/programs/validate the moment it loads and
// again by SaveProgram when it is saved. A program that needs hardware the
// chosen robot lacks says so, per rule, in the usual red.
// ---------------------------------------------------------------------------

const fileStem = (name) =>
  name.replace(/[^a-z0-9._-]+/gi, "-").replace(/^[-.]+|-+$/g, "").slice(0, 64) || "program";

function exportProgram() {
  err(""); status("");
  const name = $("name").value;
  const doc = { ...current.program, v: lang.schema_version, name };
  const url = URL.createObjectURL(new Blob([JSON.stringify(doc, null, 2) + "\n"],
    { type: "application/json" }));
  el("a", { href: url, download: `${fileStem(name)}.json` }).click();
  URL.revokeObjectURL(url);
  status(`exported ${fileStem(name)}.json`);
}

// renderable checks only the shape this editor draws. It is not validation —
// the server decides what a program *means* — but a condition node with no
// operands would throw inside renderCond before the server ever saw the file,
// and an imported file is whatever somebody handed the player.
function renderable(node, depth) {
  if (!node || typeof node !== "object" || Array.isArray(node)) return false;
  if (depth > lang.limits.max_cond_depth) return false;
  if (node.op === "and" || node.op === "or") {
    return Array.isArray(node.of) && node.of.every((k) => renderable(k, depth + 1));
  }
  // A NOT is exactly one condition. Anything else is what the server refuses,
  // and renderNot would quietly draw only the first operand of it.
  if (node.op === "not") {
    return Array.isArray(node.of) && node.of.length === 1 && renderable(node.of[0], depth + 1);
  }
  return node.op === "pred" && typeof node.pred === "string";
}

const renderableRule = (r) =>
  r && typeof r === "object" && !Array.isArray(r) && renderable(r.when, 0)
  && Array.isArray(r.then) && r.then.every((a) => a && typeof a.do === "string");

async function importProgram(file) {
  err(""); status("");
  let doc;
  try {
    doc = JSON.parse(await file.text());
  } catch (e) {
    err(`${file.name} is not valid JSON: ${e.message}`);
    return;
  }
  const v = doc && typeof doc === "object" && !Array.isArray(doc) ? (doc.v ?? lang.schema_version) : null;
  if (v === null || !Array.isArray(doc.rules)) {
    err(`${file.name} is not a program: a program is a JSON object with a "rules" list.`);
    return;
  }
  // An unknown version is refused, never guessed at: "v" exists for exactly this.
  if (v !== lang.schema_version) {
    err(`${file.name} is a version ${v} program and this build reads version ${lang.schema_version}.`);
    return;
  }
  if (!doc.rules.every(renderableRule)) {
    err(`${file.name} is malformed: every rule needs a "when" condition and a "then" action list.`);
    return;
  }
  const name = String(doc.name || fileStem(file.name.replace(/\.json$/i, "")))
    .slice(0, lang.limits.max_name_len);
  // Imported as a new, unsaved program: it must never overwrite a library row,
  // and a name already taken comes back from the save as a 409 to rename.
  current = { id: 0, name, program: { v: lang.schema_version, name, rules: doc.rules } };
  $("name").value = name;
  findings = { errors: [], warnings: [], notes: [] };
  changed(); // → /api/programs/validate against the selected blueprint
  status(`imported ${file.name} — review it, then Save`);
}

$("export").addEventListener("click", exportProgram);

$("import").addEventListener("change", async (ev) => {
  const file = ev.target.files[0];
  ev.target.value = ""; // so re-picking the same file fires again
  if (file) await importProgram(file);
});

$("save").addEventListener("click", () => save(false));
$("dup").addEventListener("click", () => save(true));
$("tryit").addEventListener("click", tryIt);

$("del").addEventListener("click", async () => {
  err("");
  if (current.id === 0) { current = blank(); $("name").value = current.name; changed(); return; }
  if (!confirm(`Delete "${current.name}"?`)) return;
  try {
    await api("DELETE", `/api/programs/${current.id}`);
    current = blank();
    $("name").value = current.name;
    await reloadLibrary();
    changed();
  } catch (e) { err(e.message); }
});

$("new").addEventListener("click", () => {
  current = blank();
  $("name").value = current.name;
  changed();
});

$("addrule").addEventListener("click", () => { current.program.rules.push(newRule()); changed(); });

$("name").addEventListener("change", () => { current.program.name = $("name").value; });

$("blueprint").addEventListener("change", () => { renderBlueprintMeta(); changed(); });

$("templates").addEventListener("change", (ev) => {
  const t = lang.templates[Number(ev.target.value)];
  ev.target.value = "";
  if (!t) return;
  // Templates carry the blueprint they were written for: the §10.7 scavenger
  // needs a parts radar, the §10.9 responder needs a weapon.
  const bp = blueprints.find((b) => b.name === t.blueprint);
  if (bp) { $("blueprint").value = String(bp.id); renderBlueprintMeta(); }
  current = { id: 0, name: t.name, program: structuredClone(t.program) };
  $("name").value = t.name;
  changed();
});

// bpcopy starts a new design from the selected one. There is no PUT: an
// approved blueprint is immutable (design §5.1), and a robot already built to
// one must not have its hardware change underneath it.
$("bpcopy").addEventListener("click", () => {
  const b = blueprints.find((x) => x.id === blueprintID());
  if (!b) return;
  picked = [...b.components];
  $("bpname").value = `${b.name} mk2`;
  $("bpnew").open = true;
  previewBlueprint();
});

$("bpcreate").addEventListener("click", async () => {
  err("");
  try {
    const bp = await api("POST", "/api/blueprints", { name: $("bpname").value, components: picked });
    if (!bp) return;
    blueprints.push(bp);
    picked = [];
    bpStats = null;
    bpStatsFor = "";
    $("bpname").value = "";
    renderBlueprints();
    $("blueprint").value = String(bp.id);
    renderBlueprintMeta();
    renderBlueprintDraft();
    changed();
  } catch (e) { err(e.message); }
});

// ---------------------------------------------------------------------------
// Boot
// ---------------------------------------------------------------------------
lang = await api("GET", "/api/language");
if (lang) {
  for (const s of lang.catalogue.predicates) preds.set(s.id, s);
  for (const s of lang.catalogue.actions) acts.set(s.id, s);
  lang.templates.forEach((t, i) => {
    $("templates").append(el("option", { value: String(i), textContent: `${t.name} (${t.section})` }));
  });
  $("name").maxLength = lang.limits.max_name_len;
  $("bpname").maxLength = lang.limits.max_name_len;

  blueprints = (await api("GET", "/api/blueprints")).blueprints;
  renderBlueprints();
  renderParts();
  renderBlueprintDraft();

  current = blank();
  $("name").value = current.name;
  await reloadLibrary();
  changed();
}
