// Localization, whole mechanism. There is no build step and no framework here,
// so the design is the smallest one that works: the keys *are* the English
// source strings (gettext style), which means English needs no dictionary at
// all — t("Ready") returns "Ready" when nothing German is behind it, and the
// HTML keeps its English text. German is a pure overlay.
//
// Markup opts in per element: `data-i18n` (key = the element's own trimmed
// text) and `data-i18n-attr="placeholder,title"` (key = each named attribute's
// own value). Nothing is translated implicitly — code samples, robot ids and
// rule-language keywords must stay English.
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
const dict =
  lang === "en"
    ? {}
    : Object.assign({}, ...(await Promise.all([load("common"), page ? load(page) : {}])));

export function t(s) {
  return dict[s] ?? s;
}

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
  const de = dict[el.textContent.trim()];
  if (de) el.textContent = de; // textContent, never innerHTML: a dictionary is data.
}
for (const el of document.querySelectorAll("[data-i18n-attr]")) {
  for (const name of el.dataset.i18nAttr.split(",")) {
    const attr = name.trim();
    const de = dict[el.getAttribute(attr)?.trim()];
    if (de) el.setAttribute(attr, de);
  }
}
// The switcher lives in the nav, whose markup is byte-identical on all eight
// pages (web/nav_test.go), so the active button is marked from here.
for (const b of document.querySelectorAll(".lang button[data-lang]")) {
  b.setAttribute("aria-pressed", String(b.dataset.lang === lang));
  b.addEventListener("click", () => setLang(b.dataset.lang));
}
