package web_test

// The match view's panels are <details class="fold">, and two things bind that
// markup to web/js/match.js by name: the fold memory is keyed on each panel's
// id, and the renderer reaches for two of those ids directly — #p-history to
// decide whether the trace panel is still worth polling, and #p-selected to
// reopen the inspector when the player picks a robot. Neither binding is
// checked at load time: a renamed or dropped id gives a dead localStorage key
// or a TypeError ten frames in, on a page that has no build step. This is the
// check.

import (
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var (
	foldRe     = regexp.MustCompile(`<details[^>]*\bclass="[^"]*\bfold\b[^"]*"[^>]*>`)
	idAttrRe   = regexp.MustCompile(`\bid="([^"]+)"`)
	panelRefRe = regexp.MustCompile(`\$\("(p-[a-z-]+)"\)`)
)

func foldPanelIDs(t *testing.T) map[string]bool {
	t.Helper()
	ids := map[string]bool{}
	for _, tag := range foldRe.FindAllString(pageHTML(t, "match.html"), -1) {
		m := idAttrRe.FindStringSubmatch(tag)
		if m == nil {
			t.Errorf("a foldable panel has no id, so its open state cannot be"+
				" remembered across a reload: %s", tag)
			continue
		}
		ids[m[1]] = true
	}
	if len(ids) < 2 {
		t.Fatalf(`match.html has %d <details class="fold"> panels; the match view folds several`, len(ids))
	}
	return ids
}

func TestFoldablePanelsAreIdentified(t *testing.T) {
	foldPanelIDs(t)
}

func TestMatchScriptOnlyReachesPanelsThatExist(t *testing.T) {
	ids := foldPanelIDs(t)
	for _, m := range panelRefRe.FindAllStringSubmatch(pageHTML(t, "js/match.js"), -1) {
		if !ids[m[1]] {
			t.Errorf("match.js reads $(%q), which is not a foldable panel in match.html."+
				" Panels there: %s", m[1], strings.Join(slices.Sorted(maps.Keys(ids)), ", "))
		}
	}
}
