package lobby

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strconv"
	"sync"

	"github.com/korjavin/robocolony/internal/db"
	"github.com/korjavin/robocolony/internal/prog"
	"github.com/korjavin/robocolony/internal/sim"
)

// Match persistence, design §2.2: a colony keeps running while its player is
// away, and a redeploy is not supposed to be an exception.
//
// Why an input log rather than a world snapshot. internal/sim is deterministic
// by construction (the E1.1 guard), and it deliberately keeps its rng, its id
// allocator and its in-flight signals unexported — they are state, they are in
// StateHash, and there is no way to read or restore them from outside the
// package. A snapshot would therefore need internal/sim to grow a
// serialisation surface over exactly the fields it hides; a replay needs
// nothing from it that is not already exported, because re-running the same
// ticks reconstructs the rng and the id counter along with everything else.
// The log is also orders of magnitude smaller on disk, which matters on the
// VPS this runs on, and it is the record E7.7's replays want anyway.
//
// The cost is CPU at startup, proportional to how far the match has run:
// measured at ~7s for 8 colonies in a 7200s match nearly over, and ~0.6s for a
// default 600s match. Duration has no ceiling, so a long match costs more. The other cost is
// that a log only replays under a build that simulates identically, which
// fingerprint below is what refuses.

// Command kinds. These strings are on disk; never repurpose one.
const (
	CmdRecall  = "recall"  // design §4.2 step 1
	CmdProgram = "program" // design §4.2 steps 3-5
)

// Command is one player input, recorded with the tick it applied at. It is the
// only thing that moves a match off its deterministic path, so the seed plus
// these plus a tick count is the whole match.
//
// ponytail: the log is unbounded, and each save rewrites all of it. A player
// hammering recall could grow it to a few hundred KB over a long match, which
// is a rate-limiting problem rather than a persistence one — there is no rate
// limiting anywhere in this server yet. Compact (drop a no-op recall, keep only
// the last install per robot) if a log is ever seen getting large.
type Command struct {
	Tick  uint64 `json:"tick"`
	Kind  string `json:"kind"`
	Robot int    `json:"robot"`

	// ProgramID is the runtime install id, and Program the program itself, for
	// CmdProgram. The program is stored verbatim rather than as a library id
	// because a player may edit that library row afterwards: replaying by id
	// would install rules the robot never actually ran.
	ProgramID string          `json:"program_id,omitempty"`
	Program   json.RawMessage `json:"program,omitempty"`
}

// apply re-applies a recorded command during a replay. Caller holds the match
// lock. It repeats the mutation the live command made and nothing else: the
// ownership and at-base checks were made when the command was accepted, and
// re-running them here would only add ways for a faithful replay to fail.
func (c Command) apply(w *sim.World, rt *prog.Runtime) error {
	r := w.RobotByID(c.Robot)
	if r == nil {
		return fmt.Errorf("no robot %d at tick %d", c.Robot, c.Tick)
	}
	switch c.Kind {
	case CmdRecall:
		r.Recalled = true
	case CmdProgram:
		p, err := prog.Decode(c.Program)
		if err != nil {
			return fmt.Errorf("robot %d: %w", c.Robot, err)
		}
		rt.Install(c.ProgramID, p)
		r.Reprogram(c.ProgramID)
	default:
		return fmt.Errorf("unknown command kind %q", c.Kind)
	}
	return nil
}

// replay rebuilds a *running* match from its lobby and its recorded input log,
// at the tick the record was written at. It is Restore's half of the job; a
// finished match's replay stops at a tick the caller chooses (Service.Replay).
//
// It is the live path exactly: newMatch generates the same arena from the same
// seat list and seed, and Match.step advances it, so the resource spawns and
// every rng draw land where they landed the first time. A restored world's
// StateHash equals the original's; TestReplayPreservesStateHash is the proof.
//
// Every failure here is a corrupt or foreign record, and the caller abandons
// the match rather than serving a world that is not the one the players left.
func replay(lobby db.Lobby, set Settings, members []db.Member, rec db.MatchLog) (*Match, error) {
	m, err := newReplay(lobby, set, members, rec)
	if err != nil {
		return nil, err
	}
	target := uint64(rec.Tick)
	// A match still marked running cannot legitimately stand at or past its
	// end: the driver finishes it at that tick. A *finished* record at exactly
	// its end is the normal case, and newReplay has already rejected one past
	// it, so the extra condition here is what keeps this path strict.
	if end := set.durationTicks(); target >= end && rec.FinishedAt.IsZero() {
		return nil, fmt.Errorf("lobby: match %d: recorded at tick %d, past its end at %d", lobby.ID, target, end)
	}
	if err := m.ReplayTo(target); err != nil {
		return nil, err
	}
	if m.replayed < len(m.log) {
		return nil, fmt.Errorf("lobby: match %d: %d commands recorded past tick %d",
			lobby.ID, len(m.log)-m.replayed, target)
	}
	return m, nil
}

// newReplay decodes a record into a freshly generated match standing at tick 0,
// with the log loaded and nothing of it applied yet. ReplayTo advances it.
func newReplay(lobby db.Lobby, set Settings, members []db.Member, rec db.MatchLog) (*Match, error) {
	var cmds []Command
	if err := json.Unmarshal([]byte(rec.Commands), &cmds); err != nil {
		return nil, fmt.Errorf("lobby: match %d: decode command log: %w", lobby.ID, err)
	}
	if rec.Tick < 0 {
		return nil, fmt.Errorf("lobby: match %d: negative tick %d", lobby.ID, rec.Tick)
	}
	if end := set.durationTicks(); uint64(rec.Tick) > end {
		return nil, fmt.Errorf("lobby: match %d: recorded at tick %d, past its end at %d", lobby.ID, rec.Tick, end)
	}
	m, err := newMatch(lobby, set, members)
	if err != nil {
		return nil, err
	}
	m.Started = rec.StartedAt
	m.log = cmds
	return m, nil
}

// ReplayTo steps a replayed match forward to target, re-applying the recorded
// commands as it passes their ticks. It is incremental: calling it with
// target+1 advances one tick, which is how the replay stream (internal/server)
// plays a finished match back frame by frame without a second protocol.
//
// Only for a match built from a stored record. On a live one it would re-apply
// commands the players have already made.
func (m *Match) ReplayTo(target uint64) error {
	stalled := false
	for {
		tick, err := m.applyDue()
		if err != nil {
			return err
		}
		if tick >= target {
			return nil
		}
		if stalled {
			// Either the match ended before the target — a record that claims
			// more ticks than its settings allow — or it was already over.
			return fmt.Errorf("lobby: match %d: ended at tick %d while replaying to %d", m.ID, tick, target)
		}
		stalled = !m.step()
	}
}

// applyDue re-applies every recorded command due at the tick the world is
// standing on, and reports that tick.
//
// A command recorded at tick t applied while the world stood at t, before the
// step that left it — so this runs before the target check in ReplayTo, and the
// commands at the target tick still apply.
func (m *Match) applyDue() (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for m.replayed < len(m.log) {
		c := m.log[m.replayed]
		if c.Tick > m.world.Tick {
			break
		}
		if c.Tick < m.world.Tick {
			return 0, fmt.Errorf("lobby: match %d: command log is out of order at tick %d", m.ID, c.Tick)
		}
		if err := c.apply(m.world, m.runtime); err != nil {
			return 0, fmt.Errorf("lobby: match %d: replay command %d: %w", m.ID, m.replayed, err)
		}
		m.replayed++
	}
	return m.world.Tick, nil
}

// fingerprint identifies this binary's simulation behaviour.
//
// An input log is only worth replaying under a build that simulates the same
// way, and this project deploys on every push: a balance change or a new
// catalogue row means yesterday's log replays into a world that never existed.
// A hand-maintained version constant would depend on whoever makes that change
// remembering to bump it, so this is computed instead, from two things that
// move whenever behaviour does:
//
//   - the state hash of a fixed mini-match, run for a fixed number of ticks.
//     Generation, the tick loop, navigation, scavenging, base production, the
//     starter kit and the program evaluator all feed it. Every AI profile is
//     seated in it as well (design §12 P2): an AI colony records no commands,
//     so its whole contribution to a replayed world is its kit and its
//     programs, and a mini-match without one would let a retuned profile
//     replay an old log into a world its players never saw.
//   - the component catalogue, which is where the balance numbers live —
//     including for the rows the mini-match never exercises, such as weapons.
//
// It cannot see a change to a rule the mini-match never reaches and the
// catalogue does not describe. That is the honest limit; the failure mode is a
// stale fingerprint, so keep the mini-match broad rather than fast if the two
// ever conflict.
var fingerprint = sync.OnceValue(func() string {
	// Fixed, not DefaultSettings: retuning the lobby defaults must not
	// invalidate every log in flight. Profiles() is not fixed in the same way —
	// but adding or removing a profile *is* a change to what a stored AI match
	// replays into, so it belongs in the fingerprint rather than outside it.
	//
	// StartingBudget is deliberately above what the built-in kit costs, so the
	// mini-match leaves a remainder and equipColony's conversion of it into
	// base stock is part of what is hashed. Set to the kit's own price it would
	// be invisible here, and a build that converts differently would accept a
	// log recorded by one that did not — the divergence would then be a colony
	// that starts with stock it never had.
	set := Settings{DurationSec: 600, Richness: 0.05, SpawnPerMin: 12, MaxPlayers: 3, Seed: 0x5eed,
		StartingBudget: 500, AI: Profiles()}
	members := []db.Member{
		{UserID: 1, DisplayName: "a"},
		{UserID: 2, DisplayName: "b"},
		// One seat brings a loadout, so the *drawn* opening roster
		// (startingRoster) is hashed too. Without it the mini-match only ever
		// equips the built-in kit, whose opening is a fixed list — and a build
		// that draws a player's opening differently would then accept a log
		// recorded by one that did not, replaying the commands into a colony
		// that started with robots it never had.
		{UserID: 3, DisplayName: "c", Loadout: fingerprintLoadout()},
	}

	h := fnv.New64a()
	m, err := newMatch(db.Lobby{ID: 0, Name: "fingerprint"}, set, members)
	if err != nil {
		// Unreachable: the settings above are constants that Validate accepts.
		// A build that cannot generate its own fingerprint match is broken, and
		// a fingerprint nothing matches is the safe answer.
		_, _ = fmt.Fprintf(h, "broken:%v", err)
	} else {
		for range fingerprintTicks {
			m.step()
		}
		_, _ = fmt.Fprintf(h, "state:%d", m.world.StateHash())
	}
	cat, err := json.Marshal(sim.Catalogue())
	if err != nil {
		_, _ = fmt.Fprintf(h, "catalogue:%v", err)
	} else {
		_, _ = h.Write(cat)
	}
	return strconv.FormatUint(h.Sum64(), 36)
})

// fingerprintTicks is long enough for the mini-match to build and deposit, and
// short enough to compute at startup: ~10ms.
const fingerprintTicks = 300

// fingerprintLoadout is the mini-match's player loadout: two scavengers that
// differ only in armor, both affordable for every draw of the opening roster.
// Two live options on purpose — a pool with one affordable entry makes the
// draw's Intn(1) return zero however it is drawn, which is the masking pattern
// docs/engineering-notes.md records twice.
//
// A nil result (unreachable: these are constants) simply seats a member with no
// loadout, which is a fingerprint that covers less rather than a wrong one.
func fingerprintLoadout() json.RawMessage {
	rules, err := DefaultProgram().Encode()
	if err != nil {
		return nil
	}
	entry := func(id int64, name string, armor sim.Variant) LoadoutEntry {
		return LoadoutEntry{
			BlueprintID: id, BlueprintName: name, ProgramID: 1, ProgramName: "scavenger",
			Components: []int{int(sim.Tracks), int(armor), int(sim.Manipulator), int(sim.PartsRadar)},
			Program:    rules,
		}
	}
	raw, err := json.Marshal(Loadout{Entries: []LoadoutEntry{
		entry(1, "medium scavenger", sim.MediumArmor),
		entry(2, "light scavenger", sim.LightArmor),
	}})
	if err != nil {
		return nil
	}
	return raw
}
