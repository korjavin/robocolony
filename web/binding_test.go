package web_test

// The client has no build step and no framework: a script reaches its markup by
// calling document.getElementById, and an id that is not there returns null.
// The failure is a TypeError in a console nobody has open — the trace panel and
// the inspector already earned fold_test.go for exactly that. This is the same
// check over every id every page's scripts reach for, which matters more now
// that graph.js is imported by two pages: it binds #graph-metric at load, so a
// page that draws the graph without carrying its markup breaks on the first
// line rather than at the first sample.

import (
	"regexp"
	"strings"
	"testing"
)

// scripts is what each page loads, transitively. graph.js appears twice on
// purpose — one module, two pages, and that is the drift this test guards.
// It must stay complete: web/i18n_test.go reads it to decide which dictionary
// a module's t(...) keys have to resolve in, and fails on a module no page
// claims.
var scripts = map[string][]string{
	"match.html":      {"js/match.js", "js/graph.js", "js/shapes.js"},
	"history.html":    {"js/history.js", "js/graph.js"},
	"editor.html":     {"js/editor.js"},
	"blueprints.html": {"js/blueprints.js", "js/shapes.js"},
}

var byIDRe = regexp.MustCompile(`\$\("([a-z0-9-]+)"\)`)

func TestScriptsOnlyReachElementsTheirPageHas(t *testing.T) {
	for page, mods := range scripts {
		html := pageHTML(t, page)
		for _, mod := range mods {
			for _, m := range byIDRe.FindAllStringSubmatch(pageHTML(t, mod), -1) {
				if !strings.Contains(html, `id="`+m[1]+`"`) {
					t.Errorf("%s reads $(%q), which %s does not carry", mod, m[1], page)
				}
			}
		}
	}
}
