// Localization, whole mechanism. There is no build step and no framework here,
// so the design is the smallest one that works: the keys *are* the English
// source strings (gettext style), which means English needs no dictionary at
// all — t("Ready") returns "Ready" when nothing German is behind it, and the
// HTML keeps its English text. German is a pure overlay.
//
// Markup opts in per element: `data-i18n` (key = the element's own text, with
// runs of whitespace collapsed to one space) and
// `data-i18n-attr="placeholder,title"` (key = each named attribute's own value,
// collapsed the same way). Nothing is translated implicitly — code samples,
// robot ids and rule-language keywords must stay English.
//
// The dictionary fetch is a top-level await, so any module importing t() is
// guaranteed a loaded dict before it renders its first row. web/i18n_test.go is
// the guard that stops a key drifting away from its German.

const LANG_KEY = "robocolony.lang";
const LANGS = ["en", "de"];

// localStorage throws outright in a few configurations (third-party iframe,
// cookies fully blocked). A dead switcher is better than a dead page.
function stored() {
  try {
    return localStorage.getItem(LANG_KEY);
  } catch {
    return null;
  }
}

const saved = stored();
export const lang = LANGS.includes(saved)
  ? saved
  : navigator.language?.startsWith("de")
    ? "de"
    : "en";

async function load(name) {
  try {
    const res = await fetch(`/js/lang/${lang}/${name}.json`);
    // A page with no dictionary of its own is not an error: pages get marked
    // up one at a time.
    return res.ok ? await res.json() : {};
  } catch {
    return {};
  }
}

const page = document.documentElement.dataset.i18nPage;
// Object.create(null), not {}: the dictionary is a lookup table, and a plain
// object answers for every name on Object.prototype as well as its own. A key
// of "toString" or "constructor" would find a *function* there — not null, not
// undefined, so ?? keeps it — and stringify into whatever the caller was
// building. With no prototype there is nothing to find but entries, which makes
// every lookup below safe by construction rather than by each one checking. It
// also makes a "__proto__" key an ordinary entry instead of a setter.
// Do not tidy this back to {}.
const dict =
  lang === "en"
    ? Object.create(null)
    : Object.assign(Object.create(null), ...(await Promise.all([load("common"), page ? load(page) : {}])));

export function t(s) {
  return dict[s] ?? s;
}

// The one place a key is not a literal in this file's sight: server errors.
// A failed response carries the English prose it always did plus the printf
// format the handler built it from and that format's arguments, and the format
// is the key — the same convention as everywhere else here, where the English
// source string is the key. So one German entry translates the whole sentence,
// arguments included.
//
// Everything degrades to English rather than to nothing: no key, no German
// behind it, no JSON body at all — each falls through to the prose the server
// already formatted, then to the status line, then to the bare code. A player
// reading "blueprint not found" in the wrong language can still act on it; a
// player reading "" or "undefined" cannot.
//
// Substitution is positional and dumb on purpose: the verb is replaced by the
// argument, whatever the verb was. ponytail: %q therefore loses the quotes Go
// would have put round it, so the German writes them itself where they matter.
// Reimplementing printf to buy that back is not the trade.
//
// The German for these keys is checked by web/i18n_test.go against the format
// strings in internal/server and internal/lobby — the guard cannot read them
// off a t() call here, so it reads them off the server instead.
export function errorText(data, res) {
  const en = data?.error || res?.statusText || `HTTP ${res?.status ?? "?"}`;
  // Anything but a string entry — no key, a key of the wrong type, a body that
  // is not an object at all — falls through to the English above.
  const de = dict[data?.key];
  if (typeof de !== "string") return en;
  const args = Array.isArray(data.args) ? data.args : [];
  let i = 0;
  // Flags, width and precision are part of the verb (%02d), so the whole thing
  // is replaced and not just its last letter. web/i18n_test.go matches verbs
  // the same way; the two have to agree, or a key can carry a verb the guard
  // cannot see and the player reads a raw "%02d".
  return de.replace(/%[-+#0]*\d*(?:\.\d+)?[a-zA-Z%]/g, (verb) => {
    if (verb === "%%") return "%";
    const v = args[i++];
    // A verb with no argument left, or one whose argument is null, stays a
    // verb: "undefined" and "null" are the two words this must never print,
    // and the untranslated English is one line below anyway.
    if (v === null || v === undefined) return verb;
    const a = String(v);
    // Vocabulary arguments ("blueprint", "program") have dictionary entries and
    // translate. ponytail: so does a blueprint a player actually named
    // "program" — the wire cannot say which is which, and the server field
    // carries the same note. Cosmetic; arg-marking is the upgrade if it bites.
    //
    // This is the one lookup whose key is player-reachable, so it is the one
    // that would have found "toString" on a prototype. It does not check for
    // that: the dictionary has no prototype to find it on. See its comment.
    return dict[a] ?? a;
  });
}

// The key a marked-up element carries: its text with the source's line wrapping
// and indentation collapsed away, so a paragraph may be wrapped for the width
// of the file and still look up the same German as one written on one line.
// web/i18n_test.go collapses the same way; the two have to agree exactly.
const key = (s) => s.trim().replace(/\s+/g, " ");

// Switching reloads. That is the feature, not a shortcut: it re-renders the
// JS-built UI for free, and there is no live re-translation to keep correct.
export function setLang(next) {
  if (!LANGS.includes(next)) return;
  try {
    localStorage.setItem(LANG_KEY, next);
  } catch {
    return; // storage is blocked: the choice cannot stick, so do not reload into the same language
  }
  location.reload();
}

// Module scripts are deferred, so the document is parsed by the time this runs.
document.documentElement.lang = lang;
for (const el of document.querySelectorAll("[data-i18n]")) {
  const de = dict[key(el.textContent)];
  if (de) el.textContent = de; // textContent, never innerHTML: a dictionary is data.
}
for (const el of document.querySelectorAll("[data-i18n-attr]")) {
  for (const name of el.dataset.i18nAttr.split(",")) {
    const attr = name.trim();
    const de = dict[key(el.getAttribute(attr) ?? "")];
    if (de) el.setAttribute(attr, de);
  }
}
// The switcher lives in the nav, whose markup is byte-identical on all eight
// pages (web/nav_test.go), so the active button is marked from here.
for (const b of document.querySelectorAll(".lang button[data-lang]")) {
  b.setAttribute("aria-pressed", String(b.dataset.lang === lang));
  b.addEventListener("click", () => setLang(b.dataset.lang));
}
