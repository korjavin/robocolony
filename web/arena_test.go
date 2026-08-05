package web_test

// The arena canvas is sized by exactly one thing: the canvas rule in
// match.html. It went wrong once (rc-w9s.38) because bakeTerrain() also set an
// inline max-width to say "never upscale past the baked size" — and an inline
// declaration beats the stylesheet, so it quietly deleted the max-width: 100%
// that was keeping the arena inside its pane. The canvas then overflowed the
// field and painted over the timeline strip.
//
// The real check is a browser measuring rects, which CI has no headless Chrome
// for. This is the cheap half: nothing in the match renderer may set an inline
// style on the canvas, and the rule that replaced the inline one must keep both
// clamps. Either half alone is how the first version passed review.

import (
	"regexp"
	"strings"
	"testing"

	"github.com/korjavin/robocolony/web"
)

func TestArenaCanvasSizedByStylesheet(t *testing.T) {
	js, err := web.FS.ReadFile("js/match.js")
	if err != nil {
		t.Fatalf("js/match.js is not embedded: %v", err)
	}
	// canvas is the module-level arena element; ctx.* drawing is untouched.
	inlineRe := regexp.MustCompile(`canvas\.style\.\w+\s*=|canvas\.style\.setProperty|canvas\.style\.cssText|canvas\.setAttribute\("style"`)
	if inline := inlineRe.FindString(string(js)); inline != "" {
		t.Errorf("js/match.js writes %s: inline styles beat the stylesheet, and the "+
			"arena's fit depends on the canvas rule in match.html winning", inline)
	}

	html, err := web.FS.ReadFile("match.html")
	if err != nil {
		t.Fatalf("match.html is not embedded: %v", err)
	}
	rule := regexp.MustCompile(`(?s)\n  canvas \{(.*?)\}`).FindStringSubmatch(string(html))
	if rule == nil {
		t.Fatal("match.html has no top-level canvas rule")
	}
	for _, want := range []string{"width: auto", "height: auto", "max-width: 100%", "max-height: 100%"} {
		if !strings.Contains(rule[1], want) {
			t.Errorf("canvas rule is missing %q; auto sizing is what never upscales past the "+
				"baked surface, and the two clamps are what keep it inside the pane — "+
				"dropping either breaks the box's aspect ratio or the fit", want)
		}
	}
}
