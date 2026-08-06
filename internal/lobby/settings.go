package lobby

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/korjavin/robocolony/internal/sim"
)

// Settings are the match settings of design §2.3. They are validated
// server-side on every path that accepts them: the client fills the form, the
// server decides what is legal.
//
// Seed is not one of them. It is drawn by the server at creation time and
// exposed read-only, so a match stays reproducible without letting a player
// shop for a favourable arena.
type Settings struct {
	DurationSec int     `json:"duration_sec"` // match length in wall-clock seconds
	Richness    float64 `json:"richness"`     // initial loose components per cell
	SpawnPerMin float64 `json:"spawn_per_min"`
	MaxPlayers  int     `json:"max_players"`
	Seed        int64   `json:"seed"`

	// StartingBudget is design §2.1 step 4's "same known starting budget" in
	// component value: what every colony, human or AI, is worth on tick one.
	// Zero means "unset" and budget() supplies the default — settings rows
	// written before this field existed decode that way and still have to
	// replay (persist.go).
	StartingBudget int `json:"starting_budget,omitempty"`

	// BarrierDensity is the fraction of cells generation turns into hard
	// barriers; the sand and rubble it paints around them scale with it, so the
	// non-open share of the arena lands around 3.9x this (measured over 100
	// seeds, epic rc-rhd). Zero means "unset" and barriers() supplies the
	// default, for the same replay reason as StartingBudget.
	BarrierDensity float64 `json:"barrier_density,omitempty"`

	// Arena is the size preset the host picked, one of arenaPresets. Empty means
	// "unset" and arenaSize() supplies M — settings rows written before this
	// field existed decode that way and must still rebuild a 64x64 world, or
	// every persisted match replays into a different arena (persist.go).
	Arena string `json:"arena,omitempty"`

	// AI is the computer colonies seated alongside the players (design §12 P2),
	// one entry per colony, in the order they take their bases after the human
	// seats. It lives in the settings rather than in lobby_members because a
	// replay rebuilds a match from the lobby row: the profile list has to come
	// back with the seed or a restart would rebuild a world with fewer colonies
	// in it. See ai.go.
	AI []Profile `json:"ai,omitempty"`
}

// Colonies is how many bases the match generates: one per seat plus one per AI
// profile.
func (s Settings) Colonies(members int) int { return members + len(s.AI) }

// Legal ranges. Only the load-bearing ones: duration, richness and budget are
// the host's taste, so they carry a floor and no ceiling.
const (
	maxSpawnPerMin         = 120
	minPlayers, maxPlayers = 1, 8

	// The floor is the price of one built-in scavenger, so the default kit
	// always fields at least one robot and no host can create a lobby that
	// cannot be started: below it startingRoster fields zero robots and
	// memberKit refuses the match at start. A bigger budget does not grow the
	// world, so there is no balance ceiling.
	minStartingBudget = 115

	// This is not a balance ceiling, it is a start-cost guard. spendRemainder
	// converts leftover budget into inventory one component at a time, so match
	// start costs O(budget) per colony: measured ~60ms at this limit, ~3.9s at a
	// billion, and an int64 straight off the wire never comes back. Far past any
	// budget a host would play, so it bounds nothing but the abuse.
	startingBudgetLimit = 10_000_000

	// The floor is 0.01 rather than 0 so that zero keeps meaning "unset"
	// (barriers reads it as the default). The ceiling is a maze that is still
	// playable: about 58% of the arena non-open.
	minBarrierDensity, maxBarrierDensity = 0.01, 0.15
	defaultBarrierDensity                = 0.08
)

// arenaPresets are the arena sizes a host may pick, square. Presets rather than
// a free width×height: a numeric pair is a validation surface and a
// denial-of-service knob for no gameplay gain.
//
// The floor clears every downstream constraint: Generate caps colonies at
// width*height/16, so the smallest preset still seats 64 colonies against a
// maxPlayers of 8.
//
// Never resize a preset without bumping the replay fingerprint (persist.go): a
// settings row records the preset by *name*, so a retune silently resizes the
// world every in-flight match created on that preset replays into. The
// fingerprint mini-match runs on the default and would not notice.
var arenaPresets = map[string]int{"XS": 32, "S": 48, "M": 64, "L": 96, "XL": 128}

// defaultArena is what an unset Arena resolves to, and the only size this game
// had before the setting existed.
const defaultArena = "M"

// arenaSize is the side of the arena this match generates. An unrecognised name
// cannot reach here — Validate rejects it — so the fallback only serves "".
func (s Settings) arenaSize() int {
	if n, ok := arenaPresets[s.Arena]; ok {
		return n
	}
	return arenaPresets[defaultArena]
}

// DefaultSettings is what the lobby form starts from.
func DefaultSettings() Settings {
	return Settings{
		DurationSec: 600,
		Richness:    0.02,
		SpawnPerMin: 6,
		MaxPlayers:  4,
		Arena:       defaultArena,

		StartingBudget: defaultStartingBudget(),
		BarrierDensity: defaultBarrierDensity,
	}
}

// budget is the starting budget this match equips its colonies from. Zero is
// the default rather than an error so that a settings row from before the
// setting existed, and the fixed fingerprint settings, keep the old opening.
func (s Settings) budget() int {
	if s.StartingBudget <= 0 {
		return defaultStartingBudget()
	}
	return s.StartingBudget
}

// barriers is the barrier density this match generates its arena from. Zero is
// the default for the same reason budget's is: a settings row from before the
// setting existed, and the fixed fingerprint settings, must keep replaying the
// arena they always generated. The negation also sends NaN to the default,
// which Validate rejects but a hand-edited row could still carry.
func (s Settings) barriers() float64 {
	if !(s.BarrierDensity > 0) {
		return defaultBarrierDensity
	}
	return s.BarrierDensity
}

// Validate reports the first out-of-range setting. Zero is not treated as
// "unset" here: a caller that wants defaults asks for DefaultSettings.
func (s Settings) Validate() error {
	switch {
	case s.DurationSec <= 0:
		// A zero-duration match would end on tick zero. Any positive length is
		// the host's business: it is wall-clock, and history decimates.
		return fmt.Errorf("duration_sec must be positive, got %d", s.DurationSec)
	case !(s.Richness >= 0): // the negation also rejects NaN
		// No ceiling: Generate clamps richness to 0..1 and spends it as a
		// target count of loose components.
		return fmt.Errorf("richness must be >= 0, got %g", s.Richness)
	case !(s.SpawnPerMin >= 0) || s.SpawnPerMin > maxSpawnPerMin:
		return fmt.Errorf("spawn_per_min must be 0..%d, got %g", maxSpawnPerMin, s.SpawnPerMin)
	case s.StartingBudget != 0 && (s.StartingBudget < minStartingBudget || s.StartingBudget > startingBudgetLimit):
		// Zero alone is exempt: it means "unset", and budget() reads it as the
		// default. Anything else the client sends has to buy at least a robot,
		// and must stay under the start-cost guard.
		return fmt.Errorf("starting_budget must be 0 or %d..%d, got %d", minStartingBudget, startingBudgetLimit, s.StartingBudget)
	case s.BarrierDensity != 0 && (!(s.BarrierDensity >= minBarrierDensity) || s.BarrierDensity > maxBarrierDensity):
		// Zero alone is exempt: it means "unset", and barriers() reads it as
		// the default. The negation also rejects NaN, which is never zero.
		//
		// Unlike duration, richness and budget (rc-8hu), this one keeps a
		// ceiling: density is not the host's taste but the shape of the board,
		// and past 0.15 the arena stops being a place two colonies can fight
		// over. repairPockets guarantees reachability, not a game.
		return fmt.Errorf("barrier_density must be %g..%g, got %g", minBarrierDensity, maxBarrierDensity, s.BarrierDensity)
	case s.Arena != "" && arenaPresets[s.Arena] == 0:
		// Empty alone is exempt: it means "unset", and arenaSize() reads it as
		// the default.
		return fmt.Errorf("arena must be empty or one of XS, S, M, L, XL, got %q", s.Arena)
	case s.MaxPlayers < minPlayers || s.MaxPlayers > maxPlayers:
		return fmt.Errorf("max_players must be %d..%d, got %d", minPlayers, maxPlayers, s.MaxPlayers)
	case s.MaxPlayers+len(s.AI) > maxPlayers:
		// maxPlayers is the cap on *colonies*, not on humans: an AI colony
		// takes a base and costs a tick's simulation exactly like a human one.
		return fmt.Errorf("%d player seats and %d AI colonies is %d colonies, the limit is %d",
			s.MaxPlayers, len(s.AI), s.MaxPlayers+len(s.AI), maxPlayers)
	}
	for _, p := range s.AI {
		if !p.Valid() {
			return fmt.Errorf("unknown AI profile %q", p)
		}
	}
	return nil
}

// GenOpts is the arena generation slice of the settings (E1.1).
func (s Settings) GenOpts(colonies int) sim.GenOpts {
	return sim.GenOpts{
		Width:          s.arenaSize(),
		Height:         s.arenaSize(),
		Colonies:       colonies,
		BarrierDensity: s.barriers(),
		Richness:       s.Richness,
	}
}

// durationTicks is the tick the match ends on.
func (s Settings) durationTicks() uint64 {
	return uint64(s.DurationSec) * TickRate
}

// spawnEvery is how many ticks pass between two resource spawns, or 0 when the
// rate is off. Design §2.3 "resource spawn rate".
func (s Settings) spawnEvery() uint64 {
	if s.SpawnPerMin <= 0 {
		return 0
	}
	return max(1, uint64(60*TickRate/s.SpawnPerMin))
}

func decodeSettings(raw string) (Settings, error) {
	var s Settings
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return Settings{}, fmt.Errorf("lobby: decode settings: %w", err)
	}
	return s, nil
}

// randomSeed draws the match seed. crypto/rand rather than math/rand: no global
// state, no seeding, and one player cannot predict the next match's arena.
func randomSeed() (int64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, fmt.Errorf("lobby: draw seed: %w", err)
	}
	return int64(binary.LittleEndian.Uint64(b[:])), nil
}
