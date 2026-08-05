package web_test

// The arena draws a robot's radar reach for the selected robot, and the reach
// is a *rule*: the wire carries state, so match.js repeats the range the way it
// already repeats VISION_RANGE. A repeated rule drifts — silently, on a page
// with no build step — and a box drawn one cell short teaches the player that a
// contact was impossible when the simulation reported it. This is the check.
//
// Nothing here restates 16 or 28. The ranges are measured out of sim by walking
// a probe away from a robot until its radar stops reporting it, which is the
// same measurement help_test.go makes for the guide's diagrams.

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/korjavin/robocolony/internal/sim"
)

var (
	radarTableRe = regexp.MustCompile(`(?s)const RADAR_RANGE = \{(.*?)\};`)
	radarEntryRe = regexp.MustCompile(`"([a-z0-9-]+)":\s*(\d+)`)
	nonSlugRe    = regexp.MustCompile(`[^a-z0-9]+`)
)

// jsSlug is match.js's slug(), which is how the renderer turns a catalogue name
// into a key. Keeping the same transform here is what makes a component rename
// fail this test instead of quietly emptying the table.
func jsSlug(s string) string {
	return nonSlugRe.ReplaceAllString(strings.ToLower(s), "-")
}

func TestRadarMarkReachesWhatTheRadarDoes(t *testing.T) {
	block := radarTableRe.FindStringSubmatch(pageHTML(t, "js/match.js"))
	if block == nil {
		t.Fatal("match.js has no RADAR_RANGE table, so nothing sizes the radar mark")
	}
	stated := map[string]int{}
	for _, m := range radarEntryRe.FindAllStringSubmatch(block[1], -1) {
		n, _ := strconv.Atoi(m[2])
		stated[m[1]] = n
	}

	// One target of every class the radars report, all standing on the probe
	// cell, so a single walk measures whichever radar the robot carries. The
	// world's robot list is trimmed back to the observer each step: a probe left
	// behind at the previous distance keeps the enemy-robot radar reporting
	// forever and the measurement runs off the end.
	place := func(w *sim.World, _, at sim.Coord) {
		w.Robots = w.Robots[:1]
		w.Loose = []*sim.LooseComponent{{ID: 7, Coord: at, Variant: sim.Manipulator}}
		w.Bases = []*sim.Base{{Colony: 3, Coord: at, Inventory: map[sim.Variant]int{}}}
		w.Robots = append(w.Robots, &sim.Robot{ID: 8, Colony: 3, Coord: at, Health: 10})
	}
	detected := func(v sim.RobotView) bool { return len(v.RadarTargets) > 0 }

	// Over the catalogue, not over a list of three: a fourth radar variant that
	// the renderer has never heard of draws no mark at all, and this is what
	// says so out loud.
	for _, c := range sim.Catalogue() {
		if c.Kind != sim.KindRadar {
			continue
		}
		t.Run(jsSlug(c.Name), func(t *testing.T) {
			bp := sim.Blueprint{Name: "mark", Components: []sim.Variant{sim.Tracks, sim.MediumArmor, c.Variant}}
			if err := bp.Validate(); err != nil {
				t.Fatalf("the probe blueprint is not legal: %v", err)
			}
			want := reach(t, bp, detected, place)
			got, ok := stated[jsSlug(c.Name)]
			if !ok {
				t.Fatalf("match.js RADAR_RANGE has no entry for %q, so a robot carrying one"+
					" gets no radar mark; the simulation reaches %d cells", c.Name, want)
			}
			if got != want {
				t.Errorf("match.js draws the %s box at %d cells, the simulation reaches %d",
					c.Name, got, want)
			}
		})
	}
}

// The legend's own stated rule is that a mark may not illustrate something the
// canvas never draws — which is exactly how this bug shipped: a design pass put
// a radar badge in the field toolbar months before anything drew one.
func TestRadarChromeOnlyClaimsWhatIsDrawn(t *testing.T) {
	if strings.Contains(pageHTML(t, "js/match.js"), "function drawRadar(") {
		if !strings.Contains(pageHTML(t, "match.html"), "Radar reach") {
			t.Error("the arena draws a radar mark and the legend does not list it")
		}
		return
	}
	if strings.Contains(pageHTML(t, "match.html"), "Radar") {
		t.Error("match.html advertises radar in the field chrome, but match.js draws no radar mark")
	}
}
