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
// parser would be a dependency bought to check nine lines of JSON. Where a
// regex genuinely cannot do the job — finding the end of a t( argument, and
// telling a comment from code — the scan is written by hand instead
// (i18nArg, i18nStrip); adding a parser to buy that back is not the trade.
//
// What the guard must NOT do is decide English prose for us. Three separate
// executors reworded copy to get past it (rc-mjj.10): an apostrophe, a
// parenthesis, a line wrap and a comment are all ordinary English, and a key
// is checked, not rationed. So: whitespace is collapsed the same way the
// runtime collapses it, the argument is scanned as a string, only the
// delimiter itself is forbidden inside a literal, and comments are stripped
// before anything is scanned. TestTheGuardItself holds each of those.

import (
	"encoding/json"
	"fmt"
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
	// Where a t(...) call starts. The argument is walked by hand from here
	// (i18nArg) rather than captured, because no regex can see that a ) or a ,
	// inside a string literal is text. The leading class keeps someObject.t(x)
	// out.
	i18nCallRe = regexp.MustCompile(`(^|[^\w$.])t\(`)
	// HTML comments, and inline scripts so JS comment rules apply to their
	// bodies and not to the markup around them.
	i18nHTMLCommentRe = regexp.MustCompile(`(?s)<!--.*?-->`)
	i18nScriptRe      = regexp.MustCompile(`(?s)(<script[^>]*>)(.*?)(</script>)`)
)

// i18nKey is the string the runtime will actually look up: web/js/i18n.js
// collapses every run of whitespace in an element's text to one space, so a
// paragraph wrapped for the width of the source file keys off the same string
// as one written on a single line. The two collapses have to agree exactly.
func i18nKey(s string) string { return strings.Join(strings.Fields(s), " ") }

// i18nArg returns the source text between a t('s parentheses, having started
// just past the open paren. Quotes are honoured, so a ) or a , inside a literal
// is not the end of anything.
func i18nArg(src string) (string, bool) {
	depth := 1
	for i := 0; i < len(src); i++ {
		switch c := src[i]; c {
		case '"', '\'', '`':
			j, ok := i18nCloseQuote(src, i)
			if !ok {
				return "", false // unterminated literal: the call never closes
			}
			i = j
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return src[:i], true
			}
		}
	}
	return "", false
}

// i18nCloseQuote finds the literal's closing delimiter, given the index of its
// opening one. Escapes are skipped, so \" does not close a " literal.
func i18nCloseQuote(src string, open int) (int, bool) {
	for i := open + 1; i < len(src); i++ {
		switch src[i] {
		case '\\':
			i++
		case src[open]:
			return i, true
		}
	}
	return 0, false
}

// i18nStripJS blanks // and /* */ comments. String and template literals are
// copied through untouched: a // inside one is a URL far more often than it is
// a comment, and a comment that happens to mention t() is not a call.
func i18nStripJS(src string) string {
	var b strings.Builder
	for i := 0; i < len(src); i++ {
		switch c := src[i]; {
		case c == '"' || c == '\'' || c == '`':
			j, ok := i18nCloseQuote(src, i)
			if !ok { // unterminated: copy the rest verbatim rather than guess
				b.WriteString(src[i:])
				return b.String()
			}
			b.WriteString(src[i : j+1])
			i = j
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			b.WriteByte('\n') // keep the line structure the error messages read
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return b.String()
			}
			b.WriteByte(' ') // a block comment separates whatever it sat between
			i += 2 + end + 1
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// i18nStrip removes the comments from a source file before anything is scanned
// out of it, so a comment mentioning t() or data-i18n is documentation and not
// a finding. JS rules apply to a .js file whole and to an .html file only
// inside its <script> elements — markup prose is full of apostrophes, and
// letting the JS scanner loose on it would have every one of them open a string.
func i18nStrip(name, src string) string {
	if !strings.HasSuffix(name, ".html") {
		return i18nStripJS(src)
	}
	src = i18nHTMLCommentRe.ReplaceAllString(src, "")
	return i18nScriptRe.ReplaceAllStringFunc(src, func(s string) string {
		m := i18nScriptRe.FindStringSubmatch(s)
		return m[1] + i18nStripJS(m[2]) + m[3]
	})
}

// i18nLiteral reads the key out of a t(...) argument, or says why it cannot.
// The key is the *runtime* string, so the source text has to be the runtime
// string too: an escape or a ${} makes them differ, and the delimiter appearing
// inside means the literal ended before the argument did. Every other
// character, apostrophes and parentheses included, is ordinary text.
func i18nLiteral(t i18nReporter, src, arg string) (string, bool) {
	a := strings.TrimSpace(arg)
	if len(a) >= 2 && strings.ContainsAny(a[:1], "\"'`") && a[len(a)-1] == a[0] {
		body := a[1 : len(a)-1]
		ok := !strings.ContainsAny(body, "\\\n"+a[:1])
		if a[0] == '`' {
			ok = ok && !strings.Contains(body, "${")
		}
		if ok {
			return body, true
		}
	}
	t.Errorf("%s: t(%s) — the key has to be a plain string literal, or nothing can check that\n"+
		"German exists behind it. Translate the literal and build the rest of the string around it.", src, a)
	return "", false
}

// i18nReporter is *testing.T, narrowed to what the scanners use so the guard's
// own self-check can hand them a collector and assert on what they said.
type i18nReporter interface {
	Errorf(format string, args ...any)
}

// i18nMarkupKeys reads the keys out of a page's markup: data-i18n elements and
// the attributes data-i18n-attr names. The source is expected to be stripped of
// comments already.
func i18nMarkupKeys(t i18nReporter, page, src string) map[string]bool {
	keys := map[string]bool{}

	// The runtime keys off el.textContent and getAttribute, both of which the
	// parser has already decoded, so &amp; here is & there.
	for _, m := range i18nTextRe.FindAllStringSubmatch(src, -1) {
		key := i18nKey(html.UnescapeString(m[1]))
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
			keys[i18nKey(html.UnescapeString(v))] = true
		}
	}
	return keys
}

// i18nCallKeys reads the keys out of every t(...) in a comment-stripped source.
func i18nCallKeys(t i18nReporter, mod, src string) map[string]bool {
	keys := map[string]bool{}
	for _, loc := range i18nCallRe.FindAllStringIndex(src, -1) {
		arg, closed := i18nArg(src[loc[1]:])
		if !closed {
			t.Errorf("%s: the t( at byte %d never closes — an unterminated string literal?", mod, loc[1])
			continue
		}
		if key, ok := i18nLiteral(t, mod, arg); ok {
			keys[key] = true
		}
	}
	return keys
}

// i18nKeys is every English string page asks to have translated: its own
// markup, its inline scripts, and the modules it loads.
func i18nKeys(t *testing.T, page string) map[string]bool {
	t.Helper()
	src := i18nStrip(page, pageHTML(t, page))
	keys := i18nMarkupKeys(t, page, src)

	// The page itself is scanned too: lobby.html's script is inline.
	maps.Copy(keys, i18nCallKeys(t, page, src))
	for _, mod := range scripts[page] {
		maps.Copy(keys, i18nCallKeys(t, mod, i18nStrip(mod, pageHTML(t, mod))))
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

// i18nCollector is an i18nReporter that keeps what it was told instead of
// failing, so the guard can be pointed at its own hard cases.
type i18nCollector []string

func (c *i18nCollector) Errorf(format string, args ...any) {
	*c = append(*c, fmt.Sprintf(format, args...))
}

// The guard decides what English the client is allowed to say, so its edges are
// worth as much care as the rules themselves. Each case below is a shape that
// either cost a real string its phrasing (the accepted half) or is real drift
// the guard exists to catch (the rejected half).
func TestTheGuardItself(t *testing.T) {
	calls := []struct {
		name    string
		file    string // "" means a .js module
		src     string
		key     string // "" means the call must be rejected
		ignored bool   // ... unless it is not a t() call at all
	}{
		{name: "an apostrophe is English, not a delimiter",
			src: `t("the match's event feed")`, key: "the match's event feed"},
		{name: "so is a parenthesis",
			src: `t("Open lobbies (waiting)")`, key: "Open lobbies (waiting)"},
		{name: "and a comma inside the literal is text, not a second argument",
			src: `t("one, two, three")`, key: "one, two, three"},
		{name: "a quote that is not the delimiter is ordinary text",
			src: `t('say "go" once')`, key: `say "go" once`},
		{name: "a comment mentioning t() is documentation",
			src: "// t(x) reads a literal\nt(\"Ready\")", key: "Ready"},
		{name: "so is a block comment mentioning it",
			src: "/* t(x), never t(`${x}`) */ t(\"Ready\")", key: "Ready"},
		{name: "a // inside a string literal is a URL, not a comment",
			src: `const u = "https://example.com/x"; t("Ready")`, key: "Ready"},
		{name: "a JS comment inside an inline script is stripped as JS",
			file: "case.html", src: "<script type=\"module\">\n// t(\"gone\")\nt(\"Ready\");\n</script>", key: "Ready"},
		{name: "a bare identifier cannot be checked from out here",
			src: `t(x)`},
		{name: "nor can a concatenation",
			src: `t("a " + b)`},
		{name: "nor can an interpolation",
			src: "t(`a ${b} c`)"},
		{name: "nor can an escape, whose source text is not its runtime string",
			src: `t("a\nb")`},
		{name: "someObject.t(x) is not this t()",
			src: `report.t(x)`, ignored: true},
	}
	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			file := c.file
			if file == "" {
				file = "case.js"
			}
			var rep i18nCollector
			got := i18nCallKeys(&rep, file, i18nStrip(file, c.src))
			switch {
			case c.ignored:
				if len(rep) != 0 || len(got) != 0 {
					t.Errorf("%s is not a t() call: keys %v, said %v", c.src, slices.Sorted(maps.Keys(got)), []string(rep))
				}
			case c.key != "":
				if len(rep) != 0 {
					t.Errorf("%s should be accepted, but the guard said: %v", c.src, []string(rep))
				}
				if want := []string{c.key}; !slices.Equal(slices.Sorted(maps.Keys(got)), want) {
					t.Errorf("%s: key is %v, want %v", c.src, slices.Sorted(maps.Keys(got)), want)
				}
			default:
				if len(rep) == 0 {
					t.Errorf("%s should be rejected — an unverifiable key is the drift the guard is for", c.src)
				}
				if len(got) != 0 {
					t.Errorf("%s was rejected but still contributed %v", c.src, slices.Sorted(maps.Keys(got)))
				}
			}
		})
	}

	markup := []struct {
		name string
		src  string
		keys []string // nil means the markup must be rejected
	}{
		{"a paragraph wrapped across three source lines is one key",
			"<p data-i18n>\n      The bars stop where the match's\n      event feed stops, so a loss older\n      than the feed is not in them.\n    </p>",
			[]string{"The bars stop where the match's event feed stops, so a loss older than the feed is not in them."}},
		{"an HTML comment mentioning data-i18n is documentation",
			"<!-- data-i18n marks an element, and the key is its text -->\n<p data-i18n>Ready</p>",
			[]string{"Ready"}},
		{"an attribute value is collapsed the same way",
			"<input data-i18n-attr=\"placeholder\"\n           placeholder=\"Lobby name\">",
			[]string{"Lobby name"}},
		{"an element that really does wrap markup is still rejected",
			`<p data-i18n>Rules <span id="rulecount">0</span></p>`, nil},
		{"and so is one with no text at all",
			`<p data-i18n></p>`, nil},
	}
	for _, c := range markup {
		t.Run(c.name, func(t *testing.T) {
			var rep i18nCollector
			got := i18nMarkupKeys(&rep, "case.html", i18nStrip("case.html", c.src))
			if c.keys == nil {
				if len(rep) == 0 {
					t.Errorf("%q should be rejected", c.src)
				}
				return
			}
			if len(rep) != 0 {
				t.Errorf("%q should be accepted, but the guard said: %v", c.src, []string(rep))
			}
			if !slices.Equal(slices.Sorted(maps.Keys(got)), c.keys) {
				t.Errorf("%q: keys are %v, want %v", c.src, slices.Sorted(maps.Keys(got)), c.keys)
			}
		})
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
