// The program editor. Plain ES module, no build step: the catalogue, the
// limits, the templates, the §6.3 constraints and every balance number come
// from the server, so this file never holds a second copy of the language or of
// the simulation's tables.
//
// Two views of one program (UX pass 1d/1e): CARDS is the editor — a rule is a
// card, the order of the cards is the program — and CODE is the same rules
// written out as text with the same per-rule verdicts in the gutter. The text
// is generated from the rules and never parsed back, so switching views cannot
// rewrite anything.
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
let view = "cards";         // "cards" | "code"
let picked = -1;            // selected line in the code view

const blank = () => ({
  id: 0, name: "new program",
  program: { v: lang.schema_version, name: "new program", rules: [] },
});
const firstPred = () => ({ op: "pred", pred: lang.catalogue.predicates[0].id });
const newRule = () => ({ when: firstPred(), then: [{ do: lang.catalogue.actions[0].id }] });
const blueprintID = () => Number($("blueprint").value || 0);

// argFor gives a catalogue row the argument its kind requires, or none.
const argFor = (spec) => (spec.arg === "none" ? {} : { arg: spec.arg === "point" ? 1 : 0 });
const predNode = (spec) => ({ op: "pred", pred: spec.id, ...argFor(spec) });
const actNode = (spec) => ({ do: spec.id, ...argFor(spec) });

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
// Catalogue rail. The parts bin the rules are built from: drag a row onto a
// rule to add it there, drop it past the end of the list to start a new rule,
// or click it, which is the same thing without a mouse gesture.
// ---------------------------------------------------------------------------

// bpKinds is what the selected robot actually carries. It greys the rows whose
// hardware is missing; the server still has the last word, per rule, in Checks.
function bpKinds() {
  const b = blueprints.find((x) => x.id === blueprintID());
  const kind = (v) => (lang.components.find((c) => c.variant === v) || {}).kind;
  return new Set((b ? b.components : []).map(kind));
}

function renderCatalogue() {
  const host = $("catalogue");
  host.replaceChildren();
  const q = $("filter").value.trim().toLowerCase();
  const have = bpKinds();
  const hit = (s) => !q || s.id.includes(q) || s.label.toLowerCase().includes(q) || s.group.includes(q);
  const section = (kind, prefix, specs) => {
    const groups = new Map();
    for (const s of specs.filter(hit)) {
      if (!groups.has(s.group)) groups.set(s.group, []);
      groups.get(s.group).push(s);
    }
    for (const [group, rows] of groups) {
      host.append(el("span", { className: "label", textContent: `${prefix} · ${group}` }));
      for (const s of rows) host.append(chip(kind, s, have));
    }
  };
  section("pred", "conditions", lang.catalogue.predicates);
  section("act", "actions", lang.catalogue.actions);
  if (!host.children.length) {
    host.append(el("p", { className: "meta", textContent: `Nothing in the language matches “${q}”.` }));
  }
}

function chip(kind, spec, have) {
  const unmet = (spec.needs || []).some((k) => !have.has(k));
  const c = el("div", {
    className: `chip${unmet ? " unmet" : ""}`,
    draggable: true,
    title: `${spec.label} — ${spec.desc}`
      + (unmet ? `\n\nNeeds ${spec.needs.join(" + ")}; this blueprint carries none.` : "")
      + "\n\nClick to add it as a new rule, or drag it onto one.",
  }, el("span", { className: "handle", textContent: "⣿" }),
     el("span", { className: "id", textContent: spec.id }));
  c.addEventListener("dragstart", (ev) => {
    drag = { kind, spec };
    ev.dataTransfer.setData("text/plain", spec.id);
    ev.dataTransfer.effectAllowed = "copy";
  });
  c.addEventListener("dragend", () => { drag = null; clearDropMarks(); });
  c.addEventListener("click", () => addFromCatalogue(kind, spec, -1));
  return c;
}

// addFromCatalogue lands a catalogue row: onto a rule, a condition ANDs into
// its WHEN and an action joins its THEN; past the end of the list, either one
// starts a new rule. The limits are the server's, read from /api/language.
function addFromCatalogue(kind, spec, i) {
  const rules = current.program.rules;
  err("");
  if (!rules[i]) {
    if (rules.length >= lang.limits.max_rules) {
      err(`a program holds at most ${lang.limits.max_rules} rules`);
      return;
    }
    rules.push(kind === "pred"
      ? { when: predNode(spec), then: [{ do: lang.catalogue.actions[0].id }] }
      : { when: firstPred(), then: [actNode(spec)] });
  } else if (kind === "pred") {
    rules[i].when = { op: "and", of: [rules[i].when, predNode(spec)] };
  } else if (rules[i].then.length >= lang.limits.max_actions_per_rule) {
    err(`a rule holds at most ${lang.limits.max_actions_per_rule} actions`);
    return;
  } else {
    rules[i].then.push(actNode(spec));
  }
  changed();
}

// ---------------------------------------------------------------------------
// Rules
// ---------------------------------------------------------------------------

function renderRules() {
  const host = $("rules");
  host.replaceChildren();
  const rules = current.program.rules;
  if (rules.length === 0) {
    host.append(el("p", { className: "meta", textContent: "No rules yet. Drag a condition in, add one, or start from a template." }));
  }
  rules.forEach((rule, i) => host.append(renderRule(rule, i, rules.length)));
  $("addrule").disabled = rules.length >= lang.limits.max_rules;
  $("rulecount").textContent = String(rules.length);
  $("ruleinfo").textContent = `${rules.length} rules`;
}

// topRule is the rule the last dry run spent most of its decisions in — the one
// actually running this robot, which is what the report exists to point at.
function topRule() {
  if (!dryrun) return -1;
  let best = -1;
  for (const r of dryrun.rules || []) {
    if (r.fired > 0 && (best < 0 || r.fired > dryrun.rules[best].fired)) best = r.rule;
  }
  return best;
}

function renderRule(rule, i, total) {
  const dead = dryrun && dryrun.never_fired.includes(i);
  // Raised off the paper: the rule taking the tick against a live robot if one
  // is being shadow-tested, and otherwise the one the last dry run spent its
  // decisions in. Both answer "which rule is running this robot"; the live one
  // answers it about a robot that exists.
  const wins = shadow ? shadow.rule === i : topRule() === i;
  const card = el("div", { className: `rule${wins ? " wins" : ""}${dead ? " dead" : ""}` });

  const body = el("div", { className: "body" },
    el("div", { className: "kw", textContent: "WHEN" }),
    renderCond(rule.when, (next) => { rule.when = next; changed(); }, null),
    el("div", { className: "kw", textContent: "THEN" }));
  rule.then.forEach((action, j) => body.append(renderAction(rule, action, j)));
  body.append(iconButton("+ action", "add an action to this rule",
    () => { rule.then.push({ do: lang.catalogue.actions[0].id }); changed(); },
    rule.then.length >= lang.limits.max_actions_per_rule));
  const sfx = summaryRow(rule, i);
  if (sfx) body.append(sfx);
  for (const is of issuesFor(i)) body.append(issueLine(is));

  // The verdict column: what you can do to this rule, and what the server and
  // the dry run make of it. A verdict belongs at the far end of the row, not
  // crowding the sentence.
  const verdict = el("div", { className: "verdict" },
    el("div", { className: "ctl" },
      iconButton("▲", "raise priority", () => move(i, -1), i === 0),
      iconButton("▼", "lower priority", () => move(i, 1), i === total - 1),
      iconButton("✕", "delete this rule", () => { current.program.rules.splice(i, 1); changed(); })));
  // A live verdict is about this robot, this tick; the dry run's badges are the
  // fallback for a program no robot is running yet.
  const live = shadowVerdict(i);
  if (live) verdict.append(live);
  else for (const b of dryRunBadges(i)) verdict.append(b);

  card.append(el("div", { className: "gutter" },
    // Padded, so the column stays one width down a list of ten or more and the
    // numbers read as the priorities they are rather than as a count.
    el("span", { className: "prio", textContent: String(i + 1).padStart(2, "0"), title: `priority ${i + 1}` }),
    dragHandle(card, i)), body, verdict);
  makeDropTarget(card, i);
  return card;
}

// summaryRow is the card's line about the whole rule: what it does besides
// taking the tick, and how often the last dry run ran it. The fired count lives
// here rather than in the verdict column because it is a measurement of the
// rule, not a verdict on it (design 1d).
function summaryRow(rule, i) {
  const sfx = rule.then.filter((a) => acts.get(a.do) && !acts.get(a.do).primary);
  const row = dryrun ? (dryrun.rules || []).find((r) => r.rule === i) : null;
  const fired = row && row.fired > 0 ? row : null;
  if (!sfx.length && !fired) return null;
  return el("div", { className: "sfx" },
    sfx.length ? el("span", {
      textContent: `side effect · ${sfx.map((a) => a.do).join(", ")}`,
      title: "these run and evaluation continues down the rule list",
    }) : null,
    el("span", { className: "grow" }),
    fired ? el("span", {
      textContent: `fired ${fired.fired}× in the last dry run`,
      title: `first at tick ${fired.first_tick}`,
    }) : null);
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
// Drag and drop (design §10.10). Native HTML5 DnD, no dependency. Two things
// are dragged into the rule list — a rule, which reorders, and a catalogue row,
// which is added — so one payload says which. The ▲▼ buttons and the click on
// a catalogue row stay: they are the keyboard path, and dragging is not.
//
// Only the handle is draggable, so a number field inside a card can still be
// selected with the mouse. The drag image is the whole card, because the card
// is what is moving.
// ---------------------------------------------------------------------------
let drag = null; // { kind: "rule", i } | { kind: "pred" | "act", spec }

function dragHandle(card, i) {
  const h = el("span", {
    className: "handle", textContent: "⣿", draggable: true,
    title: "drag to reorder — or use ▲ ▼, which the keyboard can reach",
  });
  h.addEventListener("dragstart", (ev) => {
    drag = { kind: "rule", i };
    card.classList.add("dragging");
    // Firefox refuses to start a drag without payload, even unused payload.
    ev.dataTransfer.setData("text/plain", String(i));
    ev.dataTransfer.effectAllowed = "move";
    ev.dataTransfer.setDragImage(card, 12, 12);
  });
  h.addEventListener("dragend", () => { drag = null; clearDropMarks(); });
  return h;
}

// makeDropTarget marks the card as a drop site. For a rule, which half of the
// card the pointer is in decides whether it lands above or below — rule order
// is the program, so the landing slot is drawn, never guessed. A catalogue row
// has no such choice: it goes into the rule it is dropped on.
function makeDropTarget(card, i) {
  const below = (ev) => ev.clientY > card.getBoundingClientRect().top + card.offsetHeight / 2;
  card.addEventListener("dragover", (ev) => {
    if (!drag) return;
    ev.preventDefault();
    if (drag.kind !== "rule") {
      ev.dataTransfer.dropEffect = "copy";
      card.classList.add("over-into");
      return;
    }
    ev.dataTransfer.dropEffect = "move";
    card.classList.toggle("over-below", below(ev));
    card.classList.toggle("over-above", !below(ev));
  });
  card.addEventListener("dragleave", () => card.classList.remove("over-above", "over-below", "over-into"));
  card.addEventListener("drop", (ev) => {
    if (!drag) return;
    ev.preventDefault();
    if (drag.kind === "rule") reorder(drag.i, i + (below(ev) ? 1 : 0));
    else addFromCatalogue(drag.kind, drag.spec, i);
  });
}

function clearDropMarks() {
  for (const n of document.querySelectorAll(".dragging, .over-above, .over-below, .over-into")) {
    n.classList.remove("dragging", "over-above", "over-below", "over-into");
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

// The end of the list is a drop site too: a rule dropped there becomes the last
// one, and a catalogue row dropped there starts a rule of its own.
$("droprow").addEventListener("dragover", (ev) => {
  if (!drag) return;
  ev.preventDefault();
  ev.dataTransfer.dropEffect = drag.kind === "rule" ? "move" : "copy";
  $("droprow").classList.add("over-into");
});
$("droprow").addEventListener("dragleave", () => $("droprow").classList.remove("over-into"));
$("droprow").addEventListener("drop", (ev) => {
  if (!drag) return;
  ev.preventDefault();
  if (drag.kind === "rule") reorder(drag.i, current.program.rules.length);
  else addFromCatalogue(drag.kind, drag.spec, -1);
});

// ---------------------------------------------------------------------------
// Validation feedback. Errors block the save, warnings never do (design §10.10).
// ---------------------------------------------------------------------------

// Buckets in descending loudness: an error, then a warning, then a note. Notes
// are observations about a correct program, so they are rendered quietly and
// never counted anywhere that gates a save.
const allIssues = () => [...findings.errors, ...findings.warnings, ...(findings.notes || [])];
const issuesFor = (rule) => allIssues().filter((e) => e.rule === rule);

// The server's messages name the rule they are about ("rule 3: ..."), so
// nothing here prefixes them with a number they already carry.
//
// Every one of them is written as a claim followed by the reason for it, split
// by a comma, a semicolon or a dash — so the first clause is the headline and
// the rest is the detail, and a panel of checks can be read down at a glance
// before any single one is read properly. A finding that names a rule is also
// the way to get to that rule.
function issueLine(is) {
  const at = is.message.search(/,\s|;\s|\s—\s/);
  const row = el("div", { className: `issue ${is.severity}` },
    el("div", { className: "head", textContent: at < 0 ? is.message : is.message.slice(0, at) }),
    at < 0 ? null : el("div", { className: "detail", textContent: is.message.slice(at).replace(/^\W+/, "") }));
  if (is.rule >= 0) {
    row.classList.add("jump");
    row.title = `go to rule ${is.rule + 1}`;
    row.addEventListener("click", () => revealRule(is.rule));
  }
  return row;
}

// revealRule brings a rule's card into view and flashes it. The cards are
// rebuilt on every render, so the card is looked up by position at click time
// and never held onto.
function revealRule(i) {
  if (view !== "cards") setView("cards");
  const card = $("rules").children[i];
  if (!card) return;
  card.scrollIntoView({ block: "nearest" });
  card.classList.remove("flash");
  void card.offsetWidth; // restart the animation when the same rule is clicked twice
  card.classList.add("flash");
}

function renderIssues() {
  const host = $("issues");
  host.replaceChildren();
  // The panel carries every issue, rule-scoped ones included: it is the list
  // read down before a match. Each is repeated on its own card, where it is
  // read against the rule it is about.
  for (const is of allIssues()) host.append(issueLine(is));
  if (!allIssues().length) {
    host.append(el("p", { className: "meta", style: "padding:.6rem .9rem;margin:0",
      textContent: "Nothing to say about this program." }));
  }
  const n = findings.errors.length;
  const w = findings.warnings.length;
  $("counts").replaceChildren(
    el("span", { className: `badge${n ? " solid" : ""}`, textContent: `${n} errors` }),
    el("span", { className: "badge dashed", textContent: `${w} warnings` }));
  $("save").disabled = n > 0;
  $("dup").disabled = n > 0;
  $("save").title = n > 0 ? "fix the errors first; warnings do not block saving" : "";
}

let timer = null;
function changed() {
  // Any edit invalidates the last dry run: it was measured on a program that no
  // longer exists, and after a reorder its rule numbers point at other rules.
  // The same goes for whatever the last refusal said about it, and for the
  // shadow test — which validate() re-runs a moment later, since asking again
  // is one walk down a rule list rather than a simulation.
  dryrun = null;
  shadow = null;
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
    // The shadow test rides the same debounce: it is the verdict column of the
    // cards about to be drawn, and drawing them twice would flicker every
    // verdict on every keystroke.
    await runShadow();
    if (seq !== pending) return;
    renderRules();
    renderIssues();
    renderCode();
    renderShadowHead();
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
// that took the tick — so a side-effect-only rule that ran counts as fired. The
// count for a rule that *did* fire is a measurement, and it reads on the card's
// summary row (summaryRow), not in the verdict column.
function dryRunBadges(i) {
  if (!dryrun || !dryrun.never_fired.includes(i)) return [];
  return [el("span", {
    className: "badge solid", textContent: "never fired",
    title: "in the dry run this rule never matched — an earlier rule took every tick it wanted, or its condition never held",
  })];
}

const when = (ev) => (ev.count > 0 ? `${ev.count}×, first at tick ${ev.first_tick}` : "never");

// The report is figures first and prose second: which rule is running this
// robot is a comparison, and a comparison is a set of bars, not a sentence.
function renderDryRun() {
  const host = $("dryrun");
  host.replaceChildren();
  if (!dryrun) {
    $("dryhead").textContent = "Dry run";
    host.append(el("p", { className: "meta", textContent: "Not run yet." }));
    return;
  }
  const d = dryrun;
  // The heading says which robot the numbers are about and on which seed, so a
  // report read on its own is never a report about the wrong blueprint.
  const bp = blueprints.find((x) => x.id === blueprintID());
  $("dryhead").textContent = `Dry run · ${bp ? bp.name : "blueprint"} · seed ${d.seed}`;
  // Four figures: what it was given, what it produced, what it wasted, whether
  // it lived. Everything else the report knows is said in the notes below,
  // where it can be said in a sentence instead of as a number without a unit.
  const figs = el("div", { className: "figs" });
  const fig = (k, v, title) => figs.append(
    el("span", { className: "k", textContent: k, title: title || "" }),
    el("span", { textContent: String(v) }));
  fig("ticks simulated", d.ticks);
  fig("parts deposited", d.deposited.count);
  fig("ticks idle", d.idle.count, "decisions in which no rule took the tick");
  fig("survived", d.survived ? `✓ ${d.health}/${d.max_health} hp` : `✗ tick ${d.destroyed_tick}`);
  host.append(figs);

  // Share of decisions, per rule, biggest first — plus what nothing matched,
  // because the ticks no rule wanted are the ones that cost the match.
  const rows = [...(d.rules || [])].sort((a, b) => b.fired - a.fired);
  if (rows.length && d.decisions > 0) {
    host.append(el("div", { className: "label", style: "margin:.8rem 0 .3rem", textContent: "rule firing share" }));
    for (const r of rows) host.append(shareBar(String(r.rule + 1), r.fired, d.decisions));
    if (d.idle.count > 0) host.append(shareBar("—", d.idle.count, d.decisions, "no rule matched"));
  }

  const notes = [];
  if (d.acted.count === 0) notes.push(`Never acted: in ${d.decisions} decisions no rule ever took a tick.`);
  if (d.never_fired.length > 0) {
    notes.push(`Rules that never fired: ${d.never_fired.map((i) => i + 1).join(", ")} — marked on the cards.`);
  }
  if (d.attacked.count > 0) {
    notes.push(`Attacked ${when(d.attacked)} — ${d.hit.count} of those took health off it`
      + `${d.kills > 0 ? `, destroying ${d.kills}` : ""}.`);
  }
  if (!d.survived) notes.push("Nothing after the tick it was destroyed on is your program.");
  if (d.idle.count > 0 && d.idle_reason) notes.push(`Last idle reason: ${d.idle_reason}.`);
  for (const t of notes) host.append(el("p", { className: "meta", style: "margin:.5rem 0 0", textContent: t }));

  host.append(el("p", {
    className: "meta", style: "margin:.6rem 0 0",
    textContent: `${d.width}×${d.height} practice arena. One unarmed scout `
      + "that calls out enemies it sees, against one hostile sparring partner that hunts you on "
      + "radar — so combat, defensive and signal rules all have something to match. "
      + "Two runs of the same program are always comparable; this is a smoke test, not a match.",
  }));
}

function shareBar(n, count, total, title) {
  const pct = Math.round((count / total) * 100);
  return el("div", { className: `share${count ? "" : " zero"}`, title: title || `${count} of ${total} decisions` },
    el("span", { className: "n", textContent: n }),
    el("div", { className: "track" }, el("div", { className: "fill", style: `width:${pct}%` })),
    el("span", { className: "pct", textContent: `${pct}%` }));
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
    renderCode();
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
// Live shadow test (design 1d 696-758).
//
// The dry run asks "does this program do anything at all", in a generated
// practice arena. The shadow test asks a different question — "what would these
// rules decide about the situation one of my robots is in right now" — and the
// server answers it against that robot's own perception this tick
// (POST .../robots/{id}/shadow, internal/server/explain.go).
//
// Nothing is installed and nothing is changed. The endpoint takes the match's
// read lock, evaluates the draft against a copy of the robot's view and returns
// the verdicts; the robot goes on running whatever was installed on it, and the
// only way to change that is still design §4.2's recall-and-install from the
// match page. The picker says "test", never "apply", for that reason.
//
// Finding a robot to ask about is two reads, once, at load: the running matches
// from the lobby list, and the first tick frame of each of the ones this player
// is seated in — the only place a robot's installed program is named. A robot
// is a candidate when what is installed on it is this library row, which
// internal/server's installID spells lib-<program>-r<robot>.
//
// The dry run is not replaced. It stays exactly as it was and its badges are
// what the cards carry whenever no live robot is running these rules — which is
// every program being written for the first time.
// ---------------------------------------------------------------------------

let shadow = null;    // last ShadowResult, or null
let shadowNote = "";  // why there is none, when the reason is worth saying
let liveRobots = [];  // {match, id, name, program} — this viewer's own robots

const params = new URLSearchParams(location.search);
// The match page's "reorder in editor" link names the robot it was read on.
const fromMatch = params.get("match") && params.get("robot")
  ? `${params.get("match")}:${params.get("robot")}` : "";

const botKey = (b) => `${b.match}:${b.id}`;
const ruleNo = (i) => String(i + 1).padStart(2, "0");
const botName = (r) => `${(r.archetype || "?").charAt(0).toUpperCase()}-${String(r.id).padStart(2, "0")}`;

// Only robots running the program that is open: a verdict about a robot running
// something else would be a verdict about another program's ordering.
//
// Two ids mean this library row, and the common one is the first: everything a
// colony produces runs the loadout's "lib-<program>" (internal/lobby/loadout.go)
// until somebody reprograms one robot, which installs "lib-<program>-r<robot>"
// (internal/server's installID).
const runsThisProgram = (b) =>
  b.program === `lib-${current.id}` || b.program === `lib-${current.id}-r${b.id}`;

const shadowCandidates = () =>
  (current && current.id > 0 ? liveRobots : []).filter(runsThisProgram);

const shadowTarget = () =>
  shadowCandidates().find((b) => botKey(b) === $("shadowbot").value) || null;

// firstFrame opens a match stream, keeps the init frame's colony seats and the
// first tick frame, and closes it. It is the only live world this page wants,
// and holding the stream open would cost the server a subscriber for nothing.
function firstFrame(match) {
  return new Promise((resolve) => {
    let es;
    const timer = setTimeout(() => done(null), 5000);
    const done = (v) => { clearTimeout(timer); es?.close(); resolve(v); };
    try { es = new EventSource(`/api/matches/${match}/stream`); } catch { done(null); return; }
    let colonies = [];
    const read = (ev) => { try { return JSON.parse(ev.data); } catch { return null; } };
    es.addEventListener("init", (ev) => { colonies = read(ev)?.colonies || []; });
    es.addEventListener("tick", (ev) => done({ match, colonies, robots: read(ev)?.robots || [] }));
    es.addEventListener("error", () => done(null));
  });
}

async function findLiveRobots() {
  const [lobbies, me] = await Promise.all([
    api("GET", "/api/lobbies").catch(() => null),
    api("GET", "/api/me").catch(() => null),
  ]);
  if (!me) return;
  // Matches this player is seated in. A match they are only watching has no
  // robot of theirs in it, and opening its stream to find that out would be a
  // subscriber spent on a certainty.
  const mine = (lobbies?.running || []).filter((l) => (l.members || []).some((m) => m.user_id === me.id));
  const frames = await Promise.all(mine.map((l) => firstFrame(l.id)));
  liveRobots = frames.filter(Boolean).flatMap((f) => {
    const seats = new Set(f.colonies.filter((c) => c.user_id === me.id).map((c) => c.id));
    return f.robots.filter((r) => seats.has(r.colony))
      .map((r) => ({ match: f.match, id: r.id, name: botName(r), program: r.program || "" }));
  });
}

function renderShadowPicker() {
  const sel = $("shadowbot");
  const cands = shadowCandidates();
  const keep = sel.value;
  sel.replaceChildren();
  sel.disabled = cands.length === 0;
  if (!cands.length) {
    sel.append(el("option", { value: "", textContent: "no live robot" }));
    return;
  }
  sel.append(el("option", { value: "", textContent: "shadow test off" }));
  for (const b of cands) {
    sel.append(el("option", { value: botKey(b), textContent: `${b.name} · match ${b.match}` }));
  }
  // A robot the player already picked stays picked; otherwise the one they came
  // here from, and failing that the first — a shadow test nobody has to find is
  // the whole point of the panel.
  const pick = [keep, fromMatch].find((k) => cands.some((b) => botKey(b) === k));
  sel.value = pick || botKey(cands[0]);
}

async function runShadow() {
  shadow = null;
  shadowNote = "";
  const t = shadowTarget();
  if (!t) {
    if (!shadowCandidates().length) {
      shadowNote = current && current.id > 0
        ? "no live robot is running this program"
        : "save these rules and install them to shadow-test them";
    }
    return;
  }
  try {
    shadow = await api("POST", `/api/matches/${t.match}/robots/${t.id}/shadow`,
      { program: current.program });
  } catch (e) {
    // 422: the draft does not fit that robot's blueprint, which is the same
    // refusal an install would give. 404: the robot has been destroyed since
    // the picker was built.
    shadowNote = e.status === 422
      ? `these rules do not fit ${t.name}'s blueprint`
      : `${t.name}: ${e.message}`;
  }
}

function renderShadowHead() {
  const head = $("shadowhead");
  const t = shadowTarget();
  if (!shadow || !t) { head.textContent = shadowNote; head.title = ""; return; }
  // Design's "SHADOW TEST: S-04 @ TICK 3720", plus the one thing evaluating
  // from outside a tick cannot know (ShadowResult.SignalsAssumedAbsent): a
  // tick's signals are delivered and consumed inside sim's Step, so there is no
  // inbox out here and the communication conditions are answered as if none
  // arrived.
  head.textContent = `shadow test · ${t.name} @ tick ${shadow.tick}`
    + (shadow.signals_assumed_absent ? " · signals assumed absent" : "");
  head.title = "what these rules would decide against this robot's senses this tick."
    + " Nothing is installed: the robot goes on running the program it has.";
}

// The four verdicts of design 1d, in the server's own words
// (internal/prog/explain.go): the conditions did not hold, the rule took the
// tick, it would have matched but a rule above it had already taken the tick,
// or it matched and holds only side effects, so it ran and evaluation carried
// on down the list.
function shadowVerdict(i) {
  if (!shadow) return null;
  const v = (shadow.rules || []).find((r) => r.rule === i);
  if (!v) return null;
  if (v.verdict === "shadowed") {
    return el("span", {
      className: "badge dashed",
      textContent: `✓ WOULD MATCH · SHADOWED BY ${ruleNo(v.shadowed_by)}`,
      title: `its conditions hold, but rule ${ruleNo(v.shadowed_by)} took the tick before this rule`
        + " was reached. Raise it above that rule to let it run.",
    });
  }
  if (v.verdict === "won") {
    return el("span", {
      className: "badge solid", textContent: "✓ WINS",
      title: "the first rule whose conditions hold, so it takes this tick",
    });
  }
  if (v.verdict === "ran") {
    return el("span", {
      className: "badge dashed", textContent: "✓ WOULD MATCH",
      title: "its conditions hold and it holds only side effects, so it runs and"
        + " evaluation carries on down the list",
    });
  }
  return el("span", {
    className: "meta", textContent: "✗ NOT MET",
    title: "its conditions did not hold against this robot's senses this tick",
  });
}

// ---------------------------------------------------------------------------
// Code view (UX pass 1e). The program written out as text, one row per line,
// with the same per-rule verdict in the gutter that the cards carry.
//
// One direction only: the text is generated from the rules and never parsed
// back. That is what makes "toggling views never rewrites the file" true rather
// than aspirational, and it is why there is no text parser in this codebase to
// drift from prog.Decode. Ids, not labels: this is the wire language.
// ---------------------------------------------------------------------------

const argText = (n) => (n.arg === undefined ? "" : ` ${n.arg}`);

function condText(node) {
  if (!node) return "?";
  if (node.op === "not") return `not ${parens(node.of[0])}`;
  if (node.op === "and" || node.op === "or") return (node.of || []).map(parens).join(` ${node.op} `);
  return `${node.pred}${argText(node)}`;
}
// A group inside a group gets brackets, so the picture the cards draw with
// indentation survives the flattening.
const parens = (n) => (n && (n.op === "and" || n.op === "or") ? `(${condText(n)})` : condText(n));

const condRefs = (node, out = []) => {
  if (!node) return out;
  if (node.of) node.of.forEach((k) => condRefs(k, out));
  else if (node.pred) out.push(node.pred);
  return out;
};

// mark is the gutter verdict, and it is the card's verdict said in one glyph.
// The shadow test speaks first when there is one: it is about a robot that
// exists. "~" is the mark the dry run has no equivalent of — a rule whose
// conditions hold and which never ran anyway (design 1e 848, 878-882).
function mark(i) {
  if (issuesFor(i).some((s) => s.severity !== "note")) return "!";
  if (shadow) {
    const v = (shadow.rules || []).find((r) => r.rule === i);
    if (!v) return "·";
    if (v.verdict === "shadowed") return "~";
    return v.verdict === "not_met" ? "✗" : "✓";
  }
  if (!dryrun) return "·";
  const row = (dryrun.rules || []).find((r) => r.rule === i);
  return row && row.fired > 0 ? "✓" : "✗";
}

function codeLines() {
  const out = [
    { text: `program ${current.program.name || "unnamed"} v${lang.schema_version}` },
    { text: "rules:" },
  ];
  current.program.rules.forEach((r, i) => {
    out.push({ text: `  when ${condText(r.when)}`, rule: i, refs: condRefs(r.when) });
    out.push({ text: `    then ${r.then.map((a) => `${a.do}${argText(a)}`).join(", ")}`,
               rule: i, refs: r.then.map((a) => a.do) });
  });
  if (!current.program.rules.length) out.push({ text: "  # no rules yet" });
  return out;
}

function renderCode() {
  if (view !== "code") return;
  const host = $("code");
  host.replaceChildren();
  const lines = codeLines();
  lines.forEach((line, n) => {
    // The number and the verdict go on the rule's first line only; its THEN is
    // the same rule continued, and repeating the mark would read as two.
    const first = line.rule !== undefined && (n === 0 || lines[n - 1].rule !== line.rule);
    const m = line.rule === undefined ? "" : (first ? `${String(line.rule + 1).padStart(2, "0")} ${mark(line.rule)}` : "");
    // A rule that took ticks is banded across both its lines, not only marked
    // on the first: which rule is running this robot is the question the pane
    // is read for, and a band answers it without being looked for.
    const win = line.rule !== undefined && mark(line.rule) === "✓";
    const row = el("div", { className: `cl${line.refs && line.refs.length ? " pick" : ""}${win ? " win" : ""}${picked === n ? " on" : ""}` },
      el("span", { className: "no", textContent: String(n + 1) }),
      el("span", { className: `mk${m.endsWith("✓") ? " fired" : ""}`, textContent: m }),
      el("span", { className: "tx", textContent: line.text }));
    if (line.refs && line.refs.length) {
      row.addEventListener("click", () => { picked = n; renderCode(); renderCodeDoc(line); });
    }
    host.append(row);
  });
  const n = findings.errors.length;
  $("codestatus").textContent = `schema v${lang.schema_version} · ${n ? `${n} errors` : "valid"}`;
  $("selhead").textContent = picked < 0 ? "Selection" : `Selection · line ${picked + 1}`;
}

// The documentation is the catalogue's own Desc, served by the server beside the
// evaluator that implements it, so it cannot drift from what the rule does.
function renderCodeDoc(line) {
  const host = $("codedoc");
  host.replaceChildren();
  for (const id of line.refs) {
    const spec = preds.get(id) || acts.get(id);
    host.append(el("div", { style: "margin:0 0 .6rem" },
      el("code", { textContent: id }),
      el("p", { className: "meta", style: "margin:.2rem 0 0",
        textContent: spec ? spec.desc : "not in this build's catalogue." })));
  }
}

function setView(v) {
  view = v;
  $("v-cards").setAttribute("aria-pressed", String(v === "cards"));
  $("v-code").setAttribute("aria-pressed", String(v === "code"));
  $("cardsview").hidden = v !== "cards";
  $("checksview").hidden = v !== "cards";
  $("codeview").hidden = v !== "code";
  $("codeside").hidden = v !== "code";
  renderCode();
}

// ---------------------------------------------------------------------------
// Library
// ---------------------------------------------------------------------------

function renderLibrary() {
  const sel = $("library");
  sel.replaceChildren();
  // An unsaved program is a row of its own: the picker always shows what is on
  // screen, rather than pointing at whichever saved program it started from.
  if (current.id === 0) sel.append(el("option", { value: "0", textContent: `${$("name").value || current.name} (unsaved)` }));
  for (const p of programs) {
    sel.append(el("option", { value: String(p.id), textContent: `${p.name} · ${p.program.rules.length} rules` }));
  }
  if (!programs.length && current.id !== 0) {
    // Reachable only if a read ever comes back empty: the library seeds the
    // three worked programs whenever it has none, so a player cannot strand
    // themselves by deleting everything.
    sel.append(el("option", { value: "0", textContent: "empty — start from a template" }));
  }
  sel.value = String(current.id);
}

function open(p) {
  current = { id: p.id, name: p.name, program: structuredClone(p.program) };
  $("name").value = p.name;
  findings = { errors: [], warnings: [], notes: [] };
  picked = -1;
  status("");
  changed();
}

function render() {
  renderLibrary();
  renderCatalogue();
  renderRules();
  renderIssues();
  renderDryRun();
  renderCode();
  // Which robots are candidates depends on which program is open, so the picker
  // is rebuilt with everything else.
  renderShadowPicker();
  renderShadowHead();
}

async function reloadLibrary() {
  const data = await api("GET", "/api/programs");
  if (!data) return;
  programs = data.programs;
  renderLibrary();
}

// ---------------------------------------------------------------------------
// Blueprints: which robot these rules are being written for.
//
// Only the picker and its one-line summary. Designing the robot itself is
// /blueprints (web/js/blueprints.js): the parts and their consequences needed a
// column each, and a <details> in this sidebar could show the four numbers but
// never what they meant.
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
    renderCode();
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
$("v-cards").addEventListener("click", () => setView("cards"));
$("v-code").addEventListener("click", () => setView("code"));
$("filter").addEventListener("input", renderCatalogue);

// The two keys the code view's status bar names. Ctrl/⌘+Enter is the dry run
// from anywhere on the page — it is the one action worth a chord. Alt+↑ raises
// the rule the picked line belongs to, through the same move() the ▲ button
// calls; the selection follows the rule it is on, so holding the chord walks a
// rule up the list.
document.addEventListener("keydown", (ev) => {
  if ((ev.ctrlKey || ev.metaKey) && ev.key === "Enter") {
    ev.preventDefault();
    if (!$("tryit").disabled) tryIt();
    return;
  }
  // Only in the code view: the picked line is a code-view selection, and moving
  // a rule the player cannot see it happen to is not a shortcut, it is a bug.
  if (!ev.altKey || ev.key !== "ArrowUp" || view !== "code") return;
  const lines = codeLines();
  const line = lines[picked];
  if (!line || !line.rule) return; // no selection, not a rule line, or already first
  ev.preventDefault();
  const first = (r) => lines.findIndex((l) => l.rule === r);
  picked = first(line.rule - 1) + (picked - first(line.rule));
  move(line.rule, -1);
});

$("shadowbot").addEventListener("change", async () => {
  await runShadow();
  renderRules();
  renderCode();
  renderShadowHead();
});

$("library").addEventListener("change", () => {
  const p = programs.find((x) => String(x.id) === $("library").value);
  if (p) open(p); else renderLibrary();
});

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

$("name").addEventListener("change", () => { current.program.name = $("name").value; render(); });

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

  blueprints = (await api("GET", "/api/blueprints")).blueprints;
  renderBlueprints();

  current = blank();
  $("name").value = current.name;
  await reloadLibrary();
  // /editor?program=N opens that library row. It is the link the match page's
  // sensor panel hands over, so "reorder in editor" lands on the rules the
  // robot being watched is actually running rather than on a blank program.
  const wanted = programs.find((p) => String(p.id) === params.get("program"));
  if (wanted) open(wanted); else changed(); // open() validates too

  // Last, and never awaited by anything above: the editor is usable before the
  // answer lands, and a player with no match running pays one lobby read.
  findLiveRobots().then(async () => {
    renderShadowPicker();
    await runShadow();
    renderRules();
    renderCode();
    renderShadowHead();
  });
}
