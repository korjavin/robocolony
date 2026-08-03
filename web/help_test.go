package web_test

// The rule language guide (help.html) draws what a robot perceives. A picture
// that disagrees with the simulation teaches the wrong thing more effectively
// than no picture at all, so every diagram on that page is data generated from
// sim itself, and this file regenerates it and fails when it has drifted.
//
// Nothing here is a second implementation of the geometry: the maps come out of
// sim.World.View, the same call the evaluator's predicates read. When a balance
// number moves — vision range, radar range — these tests fail and print the
// replacement map to paste in.

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/korjavin/robocolony/internal/lobby"
	"github.com/korjavin/robocolony/internal/prog"
	"github.com/korjavin/robocolony/internal/server"
	"github.com/korjavin/robocolony/internal/sim"
	"github.com/korjavin/robocolony/web"
)

// helpHTML is the page as it is actually served.
func helpHTML(t *testing.T) string {
	t.Helper()
	b, err := web.FS.ReadFile("help.html")
	if err != nil {
		t.Fatalf("help.html is not embedded: %v", err)
	}
	return string(b)
}

// perceptionBlueprints are the two robots the page draws: one without a radar
// for the vision-and-reach diagram, one with a parts radar for the radar
// diagram. Both are legal blueprints (design §6.3), so sim answers for them the
// way it would for a robot a player actually built.
func perceptionBlueprint(radar bool) sim.Blueprint {
	c := []sim.Variant{sim.Tracks, sim.MediumArmor, sim.Manipulator}
	if radar {
		c = append(c, sim.PartsRadar)
	}
	bp := sim.Blueprint{Name: "help", Components: c}
	return bp
}

// arena is a clean world big enough to hold the drawn window, with nothing in
// it but the one robot the diagrams are about.
func arena(side int) (*sim.World, sim.Coord) {
	w := sim.Generate(1, sim.GenOpts{Width: side, Height: side, Colonies: 1})
	w.Loose, w.Robots, w.Bases = nil, nil, nil
	return w, sim.Coord{X: side / 2, Y: side / 2}
}

// perceptionMap renders one cell per character, exactly as help.html stores it:
// R the robot (facing east, i.e. right), V seen in the forward cone, B seen and
// in reach, r in reach only, d on radar only, . not perceived at all. The window
// is one cell wider than radius on every side, so a range that grew would show
// up as a painted edge rather than silently fill the picture.
func perceptionMap(radius int, bp sim.Blueprint) string {
	w, c := arena(2*radius + 9)
	me := &sim.Robot{ID: 1, Colony: 9, Coord: c, Heading: sim.East, Blueprint: bp}
	w.Robots = append(w.Robots, me)
	var sb strings.Builder
	for dy := -radius - 1; dy <= radius+1; dy++ {
		for dx := -radius - 1; dx <= radius+1; dx++ {
			if dx == 0 && dy == 0 {
				sb.WriteByte('R')
				continue
			}
			// One probe component at a time: the map answers "if a component
			// were here, what would the robot know about it?", which is exactly
			// what sees_component, component_in_reach and radar_detects_target
			// ask.
			w.Loose = []*sim.LooseComponent{{ID: 99, Coord: sim.Coord{X: c.X + dx, Y: c.Y + dy}, Variant: sim.Manipulator}}
			v := w.View(me, nil)
			seen := len(v.VisibleComponents) > 0
			switch {
			case seen && v.ComponentInReach:
				sb.WriteByte('B')
			case seen:
				sb.WriteByte('V')
			case v.ComponentInReach:
				sb.WriteByte('r')
			case len(v.RadarTargets) > 0:
				sb.WriteByte('d')
			default:
				sb.WriteByte('.')
			}
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// mapIn pulls one <pre class="map" id="..."> block out of the page.
func mapIn(t *testing.T, page, id string) string {
	t.Helper()
	re := regexp.MustCompile(`(?s)<pre class="map[^"]*" id="` + id + `">\n(.*?)</pre>`)
	m := re.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("help.html has no perception map with id %q", id)
	}
	return m[1]
}

// TestPerceptionDiagramsMatchSimulation is the one that stops the guide lying.
func TestPerceptionDiagramsMatchSimulation(t *testing.T) {
	page := helpHTML(t)
	for _, tc := range []struct {
		id     string
		radius int
		radar  bool
	}{
		{"cone", 6, false},
		{"radar", 16, true},
	} {
		t.Run(tc.id, func(t *testing.T) {
			want := perceptionMap(tc.radius, perceptionBlueprint(tc.radar))
			if got := mapIn(t, page, tc.id); got != want {
				t.Errorf("the %q diagram in help.html no longer matches the simulation.\n"+
					"Replace the contents of <pre id=%q> with:\n%s\ngot:\n%s", tc.id, tc.id, want, got)
			}
		})
	}
}

// reach measures how far a sensor actually reaches, by walking a probe away
// from the robot until it stops being reported. Nothing is hardcoded: it is the
// same measurement a player would make by watching a match.
func reach(t *testing.T, bp sim.Blueprint, report func(sim.RobotView) bool, place func(*sim.World, sim.Coord, sim.Coord)) int {
	t.Helper()
	w, c := arena(96)
	me := &sim.Robot{ID: 1, Colony: 9, Coord: c, Heading: sim.East, Blueprint: bp}
	w.Robots = append(w.Robots, me)
	last := 0
	for d := 1; d < 40; d++ {
		w.Loose, w.Bases = nil, nil
		place(w, c, sim.Coord{X: c.X + d, Y: c.Y})
		if report(w.View(me, nil)) {
			last = d
		}
	}
	return last
}

func TestStatedRangesMatchSimulation(t *testing.T) {
	page := helpHTML(t)
	putLoose := func(w *sim.World, _, at sim.Coord) {
		w.Loose = []*sim.LooseComponent{{ID: 7, Coord: at, Variant: sim.Manipulator}}
	}
	seen := func(v sim.RobotView) bool { return len(v.VisibleComponents) > 0 }
	detected := func(v sim.RobotView) bool { return len(v.RadarTargets) > 0 }

	// The probe walks straight ahead, so the cone measurement is its range and
	// not its angle; the angle is what the diagram above covers.
	vision := reach(t, perceptionBlueprint(false), seen, putLoose)
	radar := reach(t, perceptionBlueprint(true), detected, putLoose)
	baseRadar := reach(t, sim.Blueprint{Name: "help", Components: []sim.Variant{sim.Tracks, sim.MediumArmor, sim.BaseRadar}},
		detected, func(w *sim.World, _, at sim.Coord) {
			w.Bases = []*sim.Base{{Colony: 3, Coord: at, Inventory: map[sim.Variant]int{}}}
		})

	for _, tc := range []struct {
		name string
		want int
	}{
		{"vision", vision},
		{"radar", radar},
		{"baseradar", baseRadar},
	} {
		re := regexp.MustCompile(`data-range="` + tc.name + `">(\d+)<`)
		m := re.FindStringSubmatch(page)
		if m == nil {
			t.Errorf("help.html states no %s range", tc.name)
			continue
		}
		if got, _ := strconv.Atoi(m[1]); got != tc.want {
			t.Errorf("help.html says the %s range is %d, the simulation reaches %d", tc.name, got, tc.want)
		}
	}
}

// TestHeadingDialMatchesSimulation checks the claim a test assumption in E1.2
// already got wrong: eight headings, 45° per turn, so looking behind you is
// four turns and not two.
func TestHeadingDialMatchesSimulation(t *testing.T) {
	page := helpHTML(t)
	named := map[string]sim.Heading{
		"N": sim.North, "NE": sim.NorthEast, "E": sim.East, "SE": sim.SouthEast,
		"S": sim.South, "SW": sim.SouthWest, "W": sim.West, "NW": sim.NorthWest,
	}
	re := regexp.MustCompile(`data-dir="([A-Z]+)" data-turns="(\d+)"`)
	found := re.FindAllStringSubmatch(page, -1)
	if len(found) != len(named) {
		t.Fatalf("the heading dial has %d directions, sim has %d", len(found), len(named))
	}
	for _, m := range found {
		h, ok := named[m[1]]
		if !ok {
			t.Errorf("the heading dial names %q, which is not a heading", m[1])
			continue
		}
		claimed, _ := strconv.Atoi(m[2])
		// Fewest turn_left or turn_right actions from east to h.
		turns := -1
		for n := 0; n <= 4; n++ {
			if sim.East.Turn(n) == h || sim.East.Turn(-n) == h {
				turns = n
				break
			}
		}
		if turns != claimed {
			t.Errorf("the dial says %s is %d turns from east, sim needs %d", m[1], claimed, turns)
		}
	}
}

// TestCountedClaimsMatchTheCatalogue guards the numbers the guide states in
// prose: three memory points, and seven side effects made of five memory writes
// and two broadcasts. They are the shape of the language, so a row added to the
// catalogue has to be reflected on the page.
func TestCountedClaimsMatchTheCatalogue(t *testing.T) {
	if sim.MemPoints != 3 {
		t.Errorf("help.html says three memory points, sim has %d", sim.MemPoints)
	}
	var mem, comms, other int
	for _, a := range prog.Language().Actions {
		if a.Primary {
			continue
		}
		switch a.Group {
		case prog.GroupMemory:
			mem++
		case prog.GroupCommunication:
			comms++
		default:
			other++
		}
	}
	if mem != 5 || comms != 2 || other != 0 {
		t.Errorf("help.html says the side effects are five memory writes and two broadcasts; "+
			"the catalogue has %d memory, %d communication and %d other", mem, comms, other)
	}
}

// TestWorkedProgramBadgesMatchValidation checks the section that teaches the
// language through its own broken examples. The guide claims §10.8 earns
// inert_start, §10.9 earns the reactive_start note, and §10.7 is clean on a
// blueprint with a radar and rejected on one without. Those claims come from
// the same templates the editor offers, decoded here rather than retyped.
func TestWorkedProgramBadgesMatchValidation(t *testing.T) {
	radarless := sim.Blueprint{Name: "no radar", Components: []sim.Variant{sim.Tracks, sim.MediumArmor, sim.Manipulator}}
	armed := sim.Blueprint{Name: "armed", Components: []sim.Variant{sim.Tracks, sim.HeavyArmor, sim.Laser, sim.PartsRadar}}
	want := map[string][]string{ // template section -> codes the guide names
		"§10.7": nil, // clean on the blueprint it is offered for
		"§10.8": {"inert_start"},
		"§10.9": {"reactive_start"},
	}
	seen := map[string]bool{}
	for _, tpl := range server.LanguageDoc().Templates {
		p, err := prog.Decode(tpl.Program)
		if err != nil {
			t.Fatalf("template %q does not decode: %v", tpl.Name, err)
		}
		bp := lobby.DefaultBlueprint() // the scavenger starter: manipulator and parts radar
		if tpl.Blueprint != "scavenger" {
			bp = armed
		}
		got := codes(prog.Validate(p, bp))
		codeWant, ok := want[tpl.Section]
		if !ok {
			t.Errorf("template %q is from %s, which the guide does not cover", tpl.Name, tpl.Section)
			continue
		}
		seen[tpl.Section] = true
		for _, c := range codeWant {
			if !slices.Contains(got, c) {
				t.Errorf("the guide says %s earns %s; validation reports %v", tpl.Section, c, got)
			}
		}
		if codeWant == nil && len(got) > 0 {
			t.Errorf("the guide says %s is clean on its own blueprint; validation reports %v", tpl.Section, got)
		}
		// The §10.7 lesson: its search step is the radar, so the same program
		// on a radarless blueprint is exactly the pair of markers the guide
		// tells the player to expect.
		if tpl.Section == "§10.7" {
			bare := codes(prog.Validate(p, radarless))
			for _, c := range []string{"missing_component", "dead_predicate"} {
				if !slices.Contains(bare, c) {
					t.Errorf("the guide says §10.7 without a radar earns %s; validation reports %v", c, bare)
				}
			}
		}
	}
	for section := range want {
		if !seen[section] {
			t.Errorf("the guide explains the %s template, which the editor no longer offers", section)
		}
	}
}

func codes(r prog.Result) []string {
	var out []string
	for _, group := range [][]prog.Issue{r.Errors, r.Warnings, r.Notes} {
		for _, i := range group {
			out = append(out, i.Code)
		}
	}
	return out
}

// TestClientDoesNotCopyCatalogueText is PR #20's rule, kept: every predicate and
// action description is served from prog.Language() and rendered at the point of
// use. A copy in the client is a copy that can drift from the evaluator, and the
// guide is the most tempting place to make one.
func TestClientDoesNotCopyCatalogueText(t *testing.T) {
	files, err := listFS(".")
	if err != nil {
		t.Fatal(err)
	}
	cat := prog.Language()
	for _, name := range files {
		b, err := web.FS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		body := string(b)
		for _, p := range cat.Predicates {
			if strings.Contains(body, p.Desc) {
				t.Errorf("%s copies the description of %s; it is served from the catalogue", name, p.ID)
			}
		}
		for _, a := range cat.Actions {
			if strings.Contains(body, a.Desc) {
				t.Errorf("%s copies the description of %s; it is served from the catalogue", name, a.ID)
			}
		}
	}
}

// listFS walks the embedded client, so a file added later is covered without
// anyone remembering to list it here.
func listFS(dir string) ([]string, error) {
	entries, err := web.FS.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if dir != "." {
			name = dir + "/" + name
		}
		if e.IsDir() {
			sub, err := listFS(name)
			if err != nil {
				return nil, err
			}
			out = append(out, sub...)
			continue
		}
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no embedded files under %q", dir)
	}
	return out, nil
}
