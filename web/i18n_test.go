package web_test

// The keys are the English source strings themselves. That is what keeps the
// mechanism to sixty lines with no build step — and it is also what makes it
// rot in silence: edit the English in the HTML and the German behind it is
// orphaned, with nothing anywhere to say so. This test is that something. It
// fails on the three shapes of drift:
//
//  1. a data-i18n key, an i18n'd attribute, or a t("...") literal with no
//     German behind it;
//  2. a dictionary entry nothing references any more;
//  3. data-i18n on an element whose text is not the whole of it — the runtime
//     key is the element's entire textContent, so nested markup means the key
//     you read in the file is not the key that gets looked up.
//
// Regex over the embedded bytes rather than an HTML parser: the markup is
// hand-written in eight files that nav_test.go already keeps regular, and a
// parser would be a dependency bought to check nine lines of JSON.

import (
	"encoding/json"
	"html"
	"io/fs"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/korjavin/robocolony/web"
)

var (
	// data-i18n, and neither data-i18n-attr nor data-i18n-page: the attribute
	// is followed by whitespace or by the end of the tag. The captured
	// character after the text says whether the text is all there is.
	i18nTextRe = regexp.MustCompile(`data-i18n(?:\s[^>]*)?>([^<]*)<(.)`)
	// A whole tag carrying data-i18n-attr, so the attributes it names can be
	// read back out of it.
	i18nTagRe  = regexp.MustCompile(`<[a-zA-Z][^>]*\sdata-i18n-attr="([^"]*)"[^>]*>`)
	i18nAttrRe = regexp.MustCompile(`([a-zA-Z-]+)="([^"]*)"`)
	// Every t(...) call, argument and all. A key that is not a plain literal
	// cannot be checked from out here, so such a call is reported rather than
	// skipped — otherwise it is exactly the shape of drift that walks past the
	// guard. The leading class keeps someObject.t(x) out.
	i18nCallRe = regexp.MustCompile(`(^|[^\w$.])t\(([^)]*)\)`)
)

// i18nLiteral reads the key out of a t(...) argument, or says why it cannot.
func i18nLiteral(t *testing.T, src, arg string) (string, bool) {
	a := strings.TrimSpace(arg)
	if len(a) >= 2 && strings.ContainsAny(a[:1], "\"'`") && a[len(a)-1] == a[0] &&
		!strings.ContainsAny(a[1:len(a)-1], "\"'`\\$") {
		return a[1 : len(a)-1], true
	}
	t.Errorf("%s: t(%s) — the key has to be a plain string literal, or nothing can check that\n"+
		"German exists behind it. Translate the literal and build the rest of the string around it.", src, a)
	return "", false
}

// i18nKeys is every English string page asks to have translated: its own
// markup, its inline scripts, and the modules it loads.
func i18nKeys(t *testing.T, page string) map[string]bool {
	t.Helper()
	keys := map[string]bool{}
	src := pageHTML(t, page)

	// The runtime keys off el.textContent and getAttribute, both of which the
	// parser has already decoded, so &amp; here is & there.
	for _, m := range i18nTextRe.FindAllStringSubmatch(src, -1) {
		key := strings.TrimSpace(html.UnescapeString(m[1]))
		switch {
		case m[2] != "/":
			t.Errorf("%s: data-i18n on an element that also holds markup (%q...).\n"+
				"The key is the element's whole textContent, so the text needs its own element.", page, key)
		case key == "":
			t.Errorf("%s: data-i18n on an element with no text, so its key is the empty string", page)
		default:
			keys[key] = true
		}
	}

	for _, tag := range i18nTagRe.FindAllStringSubmatch(src, -1) {
		attrs := map[string]string{}
		for _, a := range i18nAttrRe.FindAllStringSubmatch(tag[0], -1) {
			attrs[a[1]] = a[2]
		}
		for _, name := range strings.Split(tag[1], ",") {
			name = strings.TrimSpace(name)
			v, ok := attrs[name]
			if !ok {
				t.Errorf("%s: data-i18n-attr names %q, which the element does not carry:\n%s", page, name, tag[0])
				continue
			}
			keys[strings.TrimSpace(html.UnescapeString(v))] = true
		}
	}

	// The page itself is scanned too: lobby.html's script is inline.
	for _, mod := range append([]string{page}, scripts[page]...) {
		for _, m := range i18nCallRe.FindAllStringSubmatch(pageHTML(t, mod), -1) {
			if key, ok := i18nLiteral(t, mod, m[2]); ok {
				keys[key] = true
			}
		}
	}
	return keys
}

func i18nDict(t *testing.T, name string) map[string]string {
	t.Helper()
	b, err := web.FS.ReadFile(name)
	if err != nil {
		t.Fatalf("%s is not embedded — check the go:embed directive in embed.go: %v", name, err)
	}
	var d map[string]string
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("%s is not a flat {\"English\": \"Deutsch\"} object: %v", name, err)
	}
	return d
}

func TestTranslationsAndMarkupHaveNotDrifted(t *testing.T) {
	used := map[string]map[string]bool{}
	all := map[string]bool{}
	for _, page := range pages {
		used[page] = i18nKeys(t, page)
		maps.Copy(all, used[page])
	}

	files, err := fs.Glob(web.FS, "js/lang/*/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no dictionaries are embedded (%v); check the go:embed directive in embed.go", err)
	}

	langs := map[string]bool{}
	for _, f := range files {
		langs[path.Base(path.Dir(f))] = true
	}

	// Every key a page uses must resolve, in that language, in the shared
	// dictionary or in the page's own — the two files i18n.js merges.
	for _, lang := range slices.Sorted(maps.Keys(langs)) {
		common := i18nDict(t, "js/lang/"+lang+"/common.json")
		for _, page := range pages {
			own := map[string]string{}
			name := "js/lang/" + lang + "/" + strings.TrimSuffix(page, ".html") + ".json"
			if slices.Contains(files, name) {
				own = i18nDict(t, name)
			}
			for _, k := range slices.Sorted(maps.Keys(used[page])) {
				if _, ok := common[k]; ok {
					continue
				}
				if _, ok := own[k]; ok {
					continue
				}
				t.Errorf("%s asks for %q to be translated, but no %s entry answers it.\n"+
					"Add it to %s (or to js/lang/%s/common.json if more than one page says it).",
					page, k, lang, name, lang)
			}
		}
	}

	// And nothing may sit in a dictionary that no page says any more: that is
	// how an edit to the English orphans its German.
	for _, f := range files {
		scope := strings.TrimSuffix(path.Base(f), ".json")
		pool, where := all, "any page"
		if scope != "common" {
			var ok bool
			if pool, ok = used[scope+".html"]; !ok {
				t.Errorf("%s is a dictionary for a page that does not exist", f)
				continue
			}
			where = scope + ".html"
		}
		for _, k := range slices.Sorted(maps.Keys(i18nDict(t, f))) {
			if !pool[k] {
				t.Errorf("%s translates %q, which %s no longer says.\n"+
					"Either the English was edited and this entry is stale, or the entry is in the wrong file.", f, k, where)
			}
		}
	}
}

// The dictionaries are only checked against the modules `scripts` knows about,
// so a module nobody lists is a module whose keys nothing guards.
func TestEveryModuleIsAttributedToAPage(t *testing.T) {
	// i18n.js defines t() rather than calling it; the sample in its own comment
	// is not a key.
	mapped := map[string]bool{"js/i18n.js": true}
	for _, mods := range scripts {
		for _, m := range mods {
			mapped[m] = true
		}
	}
	files, err := fs.Glob(web.FS, "js/*.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if !mapped[f] {
			t.Errorf("no page in scripts (web/binding_test.go) lists %s, so nothing checks the keys it uses", f)
		}
	}
}

// The mechanism needs a door on every page: the module tag applies the German
// to the markup and wires the flag switcher, and data-i18n-page is what selects
// the page's half of the dictionary.
func TestEveryPageLoadsTheTranslator(t *testing.T) {
	const tag = `<script type="module" src="/js/i18n.js"></script>`
	for _, name := range pages {
		html := pageHTML(t, name)
		if !strings.Contains(html, tag) {
			t.Errorf("%s does not load the translator; expected %s in its <head>", name, tag)
		}
		if attr := `data-i18n-page="` + strings.TrimSuffix(name, ".html") + `"`; !strings.Contains(html, attr) {
			t.Errorf("%s does not carry %s on its <html> element", name, attr)
		}
	}
}
