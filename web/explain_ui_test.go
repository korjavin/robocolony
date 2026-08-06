package web_test

// The two surfaces that read the condition-level trace — the match page's
// sensor truth table and the editor's live shadow test — are the ones most
// exposed to drift from internal/prog, because both render a language they must
// not know. These are the two checks that keep them honest on a page with no
// build step.

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/korjavin/robocolony/internal/prog"
)

var (
	lineCommentRe  = regexp.MustCompile(`(?m)//.*$`)
	verdictConstRe = regexp.MustCompile(`Verdict[A-Za-z]+\s+Verdict\s*=\s*"([a-z_]+)"`)
)

// code is a script with its line comments removed, so a predicate named in
// prose is not mistaken for a predicate the renderer knows about.
func code(t *testing.T, name string) string {
	t.Helper()
	return lineCommentRe.ReplaceAllString(pageHTML(t, name), "")
}

// The truth table is every condition in the language, grouped and ordered by
// the server's catalogue, and it must stay that way: a predicate added to
// internal/prog/catalogue.go has to appear on the match page without anybody
// editing match.js. The moment one predicate id is written into the renderer —
// to give it a label, a unit or a special row — the table has a list of its own
// and the new predicate is the one that goes missing.
func TestTruthTableNamesNoPredicate(t *testing.T) {
	js := code(t, "js/match.js")
	for _, spec := range prog.Language().Predicates {
		if strings.Contains(js, string(spec.ID)) {
			t.Errorf("match.js names the predicate %q, so the truth table has a"+
				" condition list of its own — the rows must come from the catalogue"+
				" and the explain block alone", spec.ID)
		}
	}
}

// The editor's verdict column and gutter branch on the verdict strings
// internal/prog puts on the wire. A verdict added there and not here falls into
// the "✗ NOT MET" fallback, which is the one wrong answer that looks like a
// right one: a rule that would have matched, drawn as a rule that did not.
func TestShadowVerdictsAreAllRendered(t *testing.T) {
	// Read out of the source rather than listed here: a fifth verdict must fail
	// this test, and a copy of the four in the test could not notice one.
	src, err := os.ReadFile("../internal/prog/explain.go")
	if err != nil {
		t.Fatalf("reading the verdicts they are declared in: %v", err)
	}
	found := verdictConstRe.FindAllStringSubmatch(string(src), -1)
	if len(found) < 4 {
		t.Fatalf("found %d verdict constants in internal/prog/explain.go; there are four", len(found))
	}
	js := code(t, "js/editor.js")
	for _, m := range found {
		if !strings.Contains(js, `"`+m[1]+`"`) {
			t.Errorf("editor.js never tests for the %q verdict, so a rule with it is"+
				" drawn as one whose conditions did not hold", m[1])
		}
	}
}
