package web_test

// Navigation used to be an ad-hoc footer per page, and the link sets had
// drifted: the lobby reached the editor but not the guide, the guide reached
// the lobby but not the sign-in page, and the sign-in page linked nowhere at
// all — a dead end you could only leave with the back button. There is no
// template engine here and no build step, so the nav is plain markup repeated
// in six files; this test is what stops the six copies becoming six variants
// again, and what stops the seventh page shipping without one.

import (
	"regexp"
	"strings"
	"testing"

	"github.com/korjavin/robocolony/web"
)

// pages is every page the server serves. Adding one here is the cheap half of
// adding a page; the test says what the other half is.
var pages = []string{
	"index.html", "login.html", "lobby.html", "blueprints.html", "editor.html", "match.html", "help.html",
	"history.html",
}

// destinations is every route the nav must offer. /match is deliberately absent:
// a match needs an id, so it is reached from the lobbies page, which lists the
// ones that are running.
var destinations = []string{"/", "/lobby", "/blueprints", "/editor", "/history", "/help", "/login", "/auth/logout"}

var (
	navRe     = regexp.MustCompile(`(?s)<nav class="site".*?</nav>`)
	currentRe = regexp.MustCompile(` aria-current="(?:page|true)"`)
)

func pageHTML(t *testing.T, name string) string {
	t.Helper()
	b, err := web.FS.ReadFile(name)
	if err != nil {
		t.Fatalf("%s is not embedded: %v", name, err)
	}
	return string(b)
}

func TestEveryPageSharesOneNav(t *testing.T) {
	var want, from string
	for _, name := range pages {
		nav := navRe.FindString(pageHTML(t, name))
		if nav == "" {
			t.Fatalf(`%s has no <nav class="site">; every page carries the same one`, name)
		}
		// The current-page marker is the only difference two navs may have.
		bare := currentRe.ReplaceAllString(nav, "")
		if want == "" {
			want, from = bare, name
			continue
		}
		if bare != want {
			t.Errorf("the nav in %s differs from the one in %s by more than its\n"+
				"aria-current marker. They must be the same block:\n%s\ngot:\n%s",
				name, from, want, bare)
		}
	}
}

func TestEveryPageMarksWhereYouAre(t *testing.T) {
	for _, name := range pages {
		nav := navRe.FindString(pageHTML(t, name))
		if n := len(currentRe.FindAllString(nav, -1)); n != 1 {
			t.Errorf("%s marks %d nav links as current, want exactly 1", name, n)
		}
	}
}

func TestNavReachesEveryPage(t *testing.T) {
	nav := navRe.FindString(pageHTML(t, pages[0]))
	for _, dest := range destinations {
		if !strings.Contains(nav, `"`+dest+`"`) {
			t.Errorf("the nav does not offer %s, so no page reaches it", dest)
		}
	}
}

// TestEveryPageLoadsTheStylesheet is the other half of the same problem: six
// pages, six <style> blocks, and a palette that had already drifted between
// them. The shared conventions live in one file now, and a page that does not
// load it is a page free to reinvent them.
func TestEveryPageLoadsTheStylesheet(t *testing.T) {
	const link = `<link rel="stylesheet" href="/css/app.css">`
	for _, name := range pages {
		if !strings.Contains(pageHTML(t, name), link) {
			t.Errorf("%s does not link the shared stylesheet; expected %s", name, link)
		}
	}
}
