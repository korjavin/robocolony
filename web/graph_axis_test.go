package web_test

// The dashboard's y-axis labels are HTML sitting beside the graph, not <text>
// inside it: the svg is preserveAspectRatio="none", so glyphs drawn in it are
// stretched by whatever width the page gives it. That buys legible digits and
// costs a coupling — the column has to line its labels up with gridlines drawn
// in viewBox units by another file. Two numbers have to agree for that:
//
//   - the column and the svg are the same height, or the top and bottom labels
//     sit off the ends;
//   - the column's vertical padding is the graph's own PAD, as a fraction of
//     its height, or every label sits a little above or below its line.
//
// Nothing about that fails loudly. The labels just stop being true, on a page
// with no build step, and a chart with a wrong axis is worse than one with no
// axis. This is the check: history.html's declarations against graph.js's box.

import (
	"regexp"
	"strconv"
	"testing"

	"github.com/korjavin/robocolony/web"
)

var (
	graphBoxRe = regexp.MustCompile(`const W = (\d+), H = (\d+), PAD = (\d+);`)
	yAxisRe    = regexp.MustCompile(`(?s)#graph-yaxis \{(.*?)\}`)
	chartSVGRe = regexp.MustCompile(`(?s)\.chart > \.graph \{(.*?)\}`)
	heightRe   = regexp.MustCompile(`height: ([\d.]+)rem`)
	padRe      = regexp.MustCompile(`padding: ([\d.]+)rem`)
)

func rem(t *testing.T, re *regexp.Regexp, block, what string) float64 {
	t.Helper()
	m := re.FindStringSubmatch(block)
	if m == nil {
		t.Fatalf("history.html: no %s in %q", what, block)
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		t.Fatalf("history.html: %s is not a number: %v", what, err)
	}
	return v
}

func cssBlock(t *testing.T, re *regexp.Regexp, html, what string) string {
	t.Helper()
	m := re.FindStringSubmatch(html)
	if m == nil {
		t.Fatalf("history.html has no %s rule; the dashboard's chart is built from it", what)
	}
	return m[1]
}

func TestDashboardYAxisLinesUpWithTheGraphBox(t *testing.T) {
	js, err := web.FS.ReadFile("js/graph.js")
	if err != nil {
		t.Fatalf("js/graph.js is not embedded: %v", err)
	}
	box := graphBoxRe.FindStringSubmatch(string(js))
	if box == nil {
		t.Fatalf("js/graph.js no longer declares its viewBox as `const W = .., H = .., PAD = ..;`," +
			" which is what the dashboard's axis column is measured against")
	}
	h, _ := strconv.ParseFloat(box[2], 64)
	pad, _ := strconv.ParseFloat(box[3], 64)

	html := pageHTML(t, "history.html")
	axis := cssBlock(t, yAxisRe, html, "#graph-yaxis")
	svg := cssBlock(t, chartSVGRe, html, ".chart > .graph")

	axisH := rem(t, heightRe, axis, "#graph-yaxis height")
	svgH := rem(t, heightRe, svg, ".chart > .graph height")
	if axisH != svgH {
		t.Errorf("#graph-yaxis is %grem tall and the graph beside it is %grem:"+
			" the top and bottom labels cannot both be on their gridlines", axisH, svgH)
	}

	want, got := pad/h, rem(t, padRe, axis, "#graph-yaxis padding")/axisH
	if diff := want - got; diff > 0.001 || diff < -0.001 {
		t.Errorf("#graph-yaxis pads %.4f of its height, graph.js pads %.4f of its box"+
			" (PAD %g of H %g): the labels sit off their gridlines. Padding wants"+
			" to be about %.3grem.", got, want, pad, h, want*axisH)
	}
}
