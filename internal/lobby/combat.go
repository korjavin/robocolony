package lobby

import (
	"cmp"
	"slices"

	"github.com/korjavin/robocolony/internal/sim"
)

// The finished match's fight record (rc-pt6.10). The colony dashboard asks
// three questions a stored match could not answer before: how many kills and
// losses, who did the killing, and when it happened.
//
// The three come from two different places on purpose, and the difference
// matters:
//
//   - Kills, Losses and TicksActive are sim.Stats, counted by the simulation
//     itself for the whole match (internal/sim/world.go). They are COMPLETE.
//     They ride Status, so they are in the standing the history list already
//     serves as well as in the detail.
//   - KilledBy and the per-minute series are derived from the match event feed
//     (events.go), which is a ring bounded at eventCap. A match long enough to
//     fill it drops its earliest events, so the attribution and the series can
//     be PARTIAL — never wrong, only short.
//
// Nothing is stored to say how short, because nothing needs to be: the totals
// are complete and the attributed sum is not, so `Losses - sum(KilledBy)` is
// exactly the number of losses whose killer fell off the feed, and
// `Losses - sum(LossesPer)` says the same for the series. A client that wants
// to show "6 losses, 2 unattributed" subtracts; a client that does not, ignores
// it. Storing a flag would be a second copy of a derived fact.
//
// The alternative — counting attribution in the simulation so it survives the
// cap — was not taken: internal/sim already owns the complete scalars, and a
// per-attacker map per colony is state that would need a StateHash entry and a
// determinism case to buy a number the feed already carries for every match
// anyone actually plays (an ordinary 6000-tick match fills a third of the
// buffer — see eventCap).

// combatBucketTicks is one bar of the per-minute kills/losses chart: sixty
// seconds of match time at the fixed tick rate.
const combatBucketTicks = 60 * TickRate

// Combat is a match's fight record, one entry per seat in Match.Colonies order
// — the same order as Info.Colonies and History.Colonies, so a client joins
// them by index.
type Combat struct {
	// BucketTicks is the width of one KillsPer/LossesPer element, so the client
	// labels the axis without knowing this package's constants.
	BucketTicks uint64         `json:"bucket_ticks"`
	Colonies    []ColonyCombat `json:"colonies"`
}

// ColonyCombat is one colony's fight. Kills/Losses/TicksActive are complete;
// KilledBy and the two series are as complete as the event feed (see above).
type ColonyCombat struct {
	Colony sim.ColonyID `json:"colony"`

	Kills       int    `json:"kills"`        // enemy robots this colony destroyed
	Losses      int    `json:"losses"`       // own robots destroyed
	TicksActive uint64 `json:"ticks_active"` // ticks ended with a robot alive

	// KilledBy ranks the enemy robots that destroyed this colony's robots,
	// most first: the dashboard's "what killed your robots". Ties break on
	// (colony, robot) so the ranking is stable across two encodings of the
	// same match.
	KilledBy []AttackerLosses `json:"killed_by"`

	// KillsPer and LossesPer are per-bucket counts, index 0 being the first
	// minute. Both cover the whole match, zeroes included, so they are the
	// chart's x axis rather than a sparse list the client has to lay out.
	KillsPer  []int `json:"kills_per"`
	LossesPer []int `json:"losses_per"`
}

// AttackerLosses is one enemy robot and how many of this colony's robots it
// destroyed. Robot is the attacker's entity id: robots do not survive the
// match, so this is a label for the timeline, not a handle to look anything up.
type AttackerLosses struct {
	Robot  int          `json:"robot"`
	Colony sim.ColonyID `json:"colony"`
	Losses int          `json:"losses"`
}

// Combat returns the match's fight record, safe to marshal outside the match
// lock. It reads the world and the event buffer and writes neither.
func (m *Match) Combat() Combat {
	m.mu.Lock()
	defer m.mu.Unlock()

	buckets := int(m.world.Tick/combatBucketTicks) + 1
	out := Combat{BucketTicks: combatBucketTicks, Colonies: make([]ColonyCombat, len(m.Colonies))}

	// Seat index by colony id rather than assuming ColonyID is the index: the
	// mapping is newMatch's business and this must not re-derive it.
	seat := make(map[sim.ColonyID]int, len(m.Colonies))
	for i, c := range m.Colonies {
		seat[c.ID] = i
		out.Colonies[i] = ColonyCombat{
			Colony:    c.ID,
			KilledBy:  []AttackerLosses{},
			KillsPer:  make([]int, buckets),
			LossesPer: make([]int, buckets),
		}
	}
	// The complete half: the base is the colony's only singleton, so it is
	// where sim keeps the counters (internal/sim Base.Stats).
	for _, b := range m.world.Bases {
		if i, ok := seat[b.Colony]; ok {
			out.Colonies[i].Kills = b.Stats.Kills
			out.Colonies[i].Losses = b.Stats.Losses
			out.Colonies[i].TicksActive = b.Stats.TicksActive
		}
	}

	type attacker struct {
		robot  int
		colony sim.ColonyID
	}
	killers := make([]map[attacker]int, len(out.Colonies))
	for _, e := range m.events {
		if e.Kind != sim.EventLoss {
			continue
		}
		// min: an event is stamped at or before the world's current tick, so
		// this cannot clamp on a finished match. It keeps a call on a live one
		// from indexing past the array it just sized.
		b := min(int(e.Tick/combatBucketTicks), buckets-1)
		if i, ok := seat[e.Colony]; ok {
			out.Colonies[i].LossesPer[b]++
			if killers[i] == nil {
				killers[i] = map[attacker]int{}
			}
			killers[i][attacker{e.Attacker, e.AttackerColony}]++
		}
		// The same event is the attacker's kill: sim credits the killing blow
		// only, so no wreck is counted twice.
		if i, ok := seat[e.AttackerColony]; ok {
			out.Colonies[i].KillsPer[b]++
		}
	}
	for i, byRobot := range killers {
		ranked := make([]AttackerLosses, 0, len(byRobot))
		for k, n := range byRobot {
			ranked = append(ranked, AttackerLosses{Robot: k.robot, Colony: k.colony, Losses: n})
		}
		// Map iteration order is unspecified, so the sort is what makes this
		// reproducible — the same rule internal/sim lives by.
		slices.SortFunc(ranked, func(a, b AttackerLosses) int {
			return cmp.Or(
				cmp.Compare(b.Losses, a.Losses),
				cmp.Compare(a.Colony, b.Colony),
				cmp.Compare(a.Robot, b.Robot),
			)
		})
		out.Colonies[i].KilledBy = ranked
	}
	return out
}
