package lobby

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"slices"

	"github.com/korjavin/robocolony/internal/db"
	"github.com/korjavin/robocolony/internal/prog"
	"github.com/korjavin/robocolony/internal/sim"
)

// The colony a player brings to a match — design §2.1 steps 3 and 4, and the
// thing that connects the blueprint and program editors to the arena. Before
// this, every human colony was equipped from the same built-in kit and nothing
// a player designed could ever be fielded.
//
// It is per member rather than per lobby: §2.1 step 3 has *each* player pick
// from *their own* library, so it lives on the lobby_members row and not in
// Settings, which is match-wide.
//
// Two properties this file exists to keep true:
//
//   - What is stored is a snapshot, never a library id. A match replays from
//     its seed plus its command log (persist.go), and newMatch rebuilds the
//     starting robots from these bytes; if it held ids, a player editing their
//     library while the match ran would make a restart resurrect the match with
//     robots it never started with. lobby.Command already stores an installed
//     program verbatim for exactly this reason.
//   - No player can out-equip another. See startingRoster.

// maxLoadoutEntries bounds how many blueprints one colony may approve. The
// built-in kit approves eighteen (a scavenger and a base guard per locomotion ×
// armor pair), so a player fanning two of their own designs out the same way
// still fits, and nobody is capped below the default they are replacing.
const maxLoadoutEntries = 18

// maxLoadoutBytes bounds the stored snapshot. A program is capped by prog's own
// structural limits, but a full loadout of them at the library's upload limit would be
// megabytes on a row that is read back on every lobby listing.
const maxLoadoutBytes = 256 << 10

// Loadout is one member's approved blueprints, frozen. The JSON is on disk;
// never repurpose a field.
type Loadout struct {
	Entries []LoadoutEntry `json:"entries"`
}

// LoadoutEntry is one approved blueprint and the program it runs. The library
// ids are kept alongside the snapshot so the lobby screen can show the picker
// with the player's choices still selected; nothing in the match reads them.
type LoadoutEntry struct {
	BlueprintID   int64           `json:"blueprint_id"`
	BlueprintName string          `json:"blueprint_name"`
	Components    []int           `json:"components"`
	ProgramID     int64           `json:"program_id"`
	ProgramName   string          `json:"program_name"`
	Program       json.RawMessage `json:"program"`
	// Version is which version of that library program Program is a copy of —
	// the approved one, at the moment the approval was made. It is stored so
	// the editor can say which version its robots are actually running; the
	// snapshot above is still what the match runs. Zero in a loadout written
	// before versions existed, which no version of any program can match.
	Version int `json:"version,omitempty"`
}

// Choice is one approval on the wire: a blueprint from the caller's library and
// the program it should run. The server resolves both and stores the result;
// the client never sends the parts list or the rules.
type Choice struct {
	BlueprintID int64 `json:"blueprint_id"`
	ProgramID   int64 `json:"program_id"`
}

// storedBlueprint mirrors the blueprints.json column that internal/server's
// library writes. The shape is duplicated rather than shared because
// internal/server imports this package, so the dependency cannot run the other
// way; it is two fields and it is covered by a test that reads a row the
// library actually wrote.
type storedBlueprint struct {
	Components []int `json:"components"`
}

// blueprint is the entry as the simulation sees it.
//
// The ids are namespaced away from the built-in kit's "bp-default-*" and
// "bp-ai-*", and away from the per-robot install id internal/server's reprogram
// command builds on top of ProgramRuntimeID: a colony-wide install and a
// one-robot install of the same library program must not collide in the
// runtime.
func (e LoadoutEntry) blueprint() sim.Blueprint {
	return sim.Blueprint{
		ID:         fmt.Sprintf("bp-lib-%d", e.BlueprintID),
		Name:       e.BlueprintName,
		Components: toVariants(e.Components),
		ProgramID:  ProgramRuntimeID(e.ProgramID, e.Version),
	}
}

// ProgramRuntimeID is the runtime id one version of a library program is
// installed under. The version is in the id because that is what makes a
// robot's program legible after the fact: "in use by 6 robots" is a question
// about a version, and the only record of what a running robot was given is
// this string (sim.Robot.ProgramID).
//
// internal/server's reprogram command appends "-r<robot>" to it, so a one-robot
// install is still recognisably the same program and version. ProgramRef reads
// either form back.
func ProgramRuntimeID(programID int64, version int) string {
	return fmt.Sprintf("lib-%d-v%d", programID, version)
}

// ProgramRef splits a runtime program id back into the library program and
// version it was installed from. ok is false for anything that did not come out
// of a library — the built-in kit's ids, and the AI profiles'.
func ProgramRef(runtimeID string) (programID int64, version int, ok bool) {
	// Sscanf stops at the first character %d cannot use, so the "-r<robot>" a
	// per-robot install appends is left unread rather than misparsed.
	n, err := fmt.Sscanf(runtimeID, "lib-%d-v%d", &programID, &version)
	return programID, version, err == nil && n == 2
}

// toVariants converts stored component numbers. An out-of-range number becomes
// VariantNone, which sim.Blueprint.Validate then rejects as unknown.
func toVariants(in []int) []sim.Variant {
	out := make([]sim.Variant, 0, len(in))
	for _, v := range in {
		if v < 0 || v > 255 {
			v = int(sim.VariantNone)
		}
		out = append(out, sim.Variant(v))
	}
	return out
}

// memberKit is the colony a seat starts with: the member's own loadout, or the
// built-in kit when they chose nothing. The fallback is what every colony did
// before this feature, so a player who never opens the picker is never blocked.
//
// A stored loadout that will not rebuild is an error rather than a silent
// fallback: it can only mean a corrupt row or a build whose language no longer
// decodes these rules, and starting a match with robots the player did not
// choose is worse than refusing to start it.
func memberKit(m db.Member, budget int) (kit, error) {
	built := humanKit()
	built.budget = budget
	if len(m.Loadout) == 0 {
		return built, nil
	}
	var l Loadout
	if err := json.Unmarshal(m.Loadout, &l); err != nil {
		return kit{}, fmt.Errorf("lobby: member %d: decode loadout: %w", m.UserID, err)
	}
	if len(l.Entries) == 0 {
		return built, nil
	}
	if len(l.Entries) > maxLoadoutEntries {
		return kit{}, fmt.Errorf("lobby: member %d: loadout approves %d blueprints, the limit is %d",
			m.UserID, len(l.Entries), maxLoadoutEntries)
	}

	k := kit{budget: budget}
	for _, e := range l.Entries {
		bp := e.blueprint()
		if err := bp.Validate(); err != nil {
			return kit{}, fmt.Errorf("lobby: member %d: blueprint %q: %w", m.UserID, e.BlueprintName, err)
		}
		p, err := prog.Decode(e.Program)
		if err != nil {
			return kit{}, fmt.Errorf("lobby: member %d: program %q: %w", m.UserID, e.ProgramName, err)
		}
		k.blueprints = append(k.blueprints, bp)
		// Installing the same id twice is a map write, so two blueprints sharing
		// a program need no dedupe here.
		k.programs = append(k.programs, namedProgram{bp.ProgramID, p})
	}
	// The roster itself is drawn in equipColony, which is the first place with a
	// world and therefore an rng (see startingRoster). What is checked here is
	// only that the draw will have something to draw: a loadout whose *cheapest*
	// approval costs more than the whole budget the host set would field no
	// robots at all, and a colony with no robots and no inventory can never act
	// again (design §5.3). It has to fail before an arena is generated.
	k.choices = k.blueprints
	if !slices.ContainsFunc(k.choices, func(bp sim.Blueprint) bool { return bp.Value() <= budget }) {
		return kit{}, fmt.Errorf("lobby: member %d: no approved blueprint fits the starting budget", m.UserID)
	}
	return k, nil
}

// defaultStartingBudget is what Settings.StartingBudget defaults to, priced in
// the only currency the simulation has: component value, the same number the
// design §9 score is built from.
//
// It is defined as what the built-in kit costs, so a host who never touches the
// setting gets exactly the opening this game has always had.
func defaultStartingBudget() int { return startingRobots * DefaultBlueprint().Value() }

// startingRoster is the robots a colony takes into the arena, and the whole of
// the "nobody out-equips anybody" guarantee.
//
// Each robot is drawn uniformly from the approvals that still fit what is left
// of the budget, so a player who approves a mixed set fields a mixed opening
// (rc-w9s.36) instead of three copies of whichever design happened to be first.
// The draw is on the world's rng, never math/rand: the opening roster is world
// state at tick zero, and a replay rebuilds it from the seed alone
// (persist.go). That is also why this runs from equipColony rather than
// memberKit — memberKit resolves kits before sim.Generate, where there is no
// world to draw from.
//
// The budget is what bounds it in both directions:
//
//   - never more than startingRobots units, so approving a cheap body cannot
//     buy a bigger colony;
//   - never more than the match's budget in component value, so approving an
//     expensive one cannot buy a stronger colony. Two heavy gunners cost about
//     what three unarmed scavengers do, and that is the trade the player makes
//     rather than a way around the cap.
//
// What is left over is not wasted: equipColony converts it into base inventory
// (design §12 P0), so spending less than the budget buys spares rather than
// nothing.
// DefaultStartingBudget is Settings.StartingBudget's default, and StartingFleet
// is how many robots a colony would open with if this design were its only
// approval. Both exist for the blueprint configurator: "the starting budget
// fields three of these, or two of that" is the trade a player is actually
// making, and it has to be answered by the code that decides it rather than by
// arithmetic in the browser.
//
// The rng is a throwaway and that is sound rather than a shortcut: with exactly
// one choice every draw returns that choice, so the count is a pure function of
// value and budget. Going through startingRoster anyway is the whole point —
// the startingRobots cap and the budget subtraction are its rules, and a second
// copy of them here would be exactly the drift the configurator exists to avoid.
func DefaultStartingBudget() int { return defaultStartingBudget() }

func StartingFleet(bp sim.Blueprint, budget int) int {
	return len(startingRoster(rand.New(rand.NewSource(1)), []sim.Blueprint{bp}, budget))
}

func startingRoster(rng *rand.Rand, choices []sim.Blueprint, budget int) []sim.Blueprint {
	out := make([]sim.Blueprint, 0, startingRobots)
	fits := make([]sim.Blueprint, 0, len(choices))
	for range startingRobots {
		fits = fits[:0]
		for _, bp := range choices {
			if bp.Value() <= budget {
				fits = append(fits, bp)
			}
		}
		if len(fits) == 0 {
			return out
		}
		bp := fits[rng.Intn(len(fits))]
		out = append(out, bp)
		budget -= bp.Value()
	}
	return out
}
