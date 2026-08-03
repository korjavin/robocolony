package lobby

import (
	"encoding/json"
	"fmt"

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
// "bp-ai-*", and away from the per-robot "lib-%d-r%d" that internal/server's
// reprogram command installs under: a colony-wide install and a one-robot
// install of the same library program must not collide in the runtime.
func (e LoadoutEntry) blueprint() sim.Blueprint {
	return sim.Blueprint{
		ID:         fmt.Sprintf("bp-lib-%d", e.BlueprintID),
		Name:       e.BlueprintName,
		Components: toVariants(e.Components),
		ProgramID:  fmt.Sprintf("lib-%d", e.ProgramID),
	}
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
func memberKit(m db.Member) (kit, error) {
	if len(m.Loadout) == 0 {
		return humanKit(), nil
	}
	var l Loadout
	if err := json.Unmarshal(m.Loadout, &l); err != nil {
		return kit{}, fmt.Errorf("lobby: member %d: decode loadout: %w", m.UserID, err)
	}
	if len(l.Entries) == 0 {
		return humanKit(), nil
	}
	if len(l.Entries) > maxLoadoutEntries {
		return kit{}, fmt.Errorf("lobby: member %d: loadout approves %d blueprints, the limit is %d",
			m.UserID, len(l.Entries), maxLoadoutEntries)
	}

	var k kit
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
	k.start = startingRoster(k.blueprints)
	if len(k.start) == 0 {
		// Unreachable while the most expensive legal robot costs less than the
		// whole budget (see startingBudget). A colony with no robots and no
		// inventory can never act again (design §5.3), so refuse rather than
		// field one.
		return kit{}, fmt.Errorf("lobby: member %d: no approved blueprint fits the starting budget", m.UserID)
	}
	return k, nil
}

// startingBudget is design §2.1 step 4's "same known starting budget", priced
// in the only currency the simulation has: component value, the same number the
// design §9 score is built from.
//
// It is defined as what the built-in kit costs, so the default colony spends it
// exactly and this feature cannot make anyone stronger than the game already
// was — only differently equipped.
func startingBudget() int { return startingRobots * DefaultBlueprint().Value() }

// startingRoster is the robots a colony takes into the arena, and the whole of
// the "nobody out-equips anybody" guarantee.
//
// The first approved blueprint is the starting body, repeated — the same shape
// humanKit has always had, where the whole opening is three of one design and
// the rest of the approved set exists for §5.2 production. Approving more
// designs therefore never costs a player robots; it only widens what the base
// can build once parts start arriving.
//
// The budget is what bounds it in both directions:
//
//   - never more than startingRobots units, so approving a cheap body cannot
//     buy a bigger colony;
//   - never more than startingBudget in component value, so approving an
//     expensive one cannot buy a stronger colony. Two heavy gunners cost about
//     what three unarmed scavengers do, and that is the trade the player makes
//     rather than a way around the cap.
func startingRoster(bps []sim.Blueprint) []sim.Blueprint {
	bp := bps[0]
	n := startingRobots
	if v := bp.Value(); v > 0 {
		n = min(n, startingBudget()/v)
	}
	return repeat(bp, n)
}
