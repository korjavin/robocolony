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

// Legal ranges. Wide enough to be worth tuning, narrow enough that no setting
// can turn one match into a denial of service for the whole server.
const (
	minDurationSec, maxDurationSec = 60, 7200
	minRichness, maxRichness       = 0.001, 0.25
	maxSpawnPerMin                 = 120
	minPlayers, maxPlayers         = 1, 8

	// The floor is the price of one built-in scavenger, so the default kit
	// always fields at least one robot and no host can create a lobby that
	// cannot be started (see startingRoster). The ceiling is ten times the
	// default: enough for a rich skirmish, and it bounds the leftover
	// conversion at a few hundred components per base (see spendRemainder).
	minStartingBudget, maxStartingBudget = 115, 3450

	// The arena is fixed for the POC: design §2.3 has no size setting, and
	// generation needs room for maxPlayers bases (Generate caps colonies at
	// width*height/16).
	arenaWidth, arenaHeight = 64, 64
	barrierDensity          = 0.08
)

// DefaultSettings is what the lobby form starts from.
func DefaultSettings() Settings {
	return Settings{
		DurationSec: 600,
		Richness:    0.02,
		SpawnPerMin: 6,
		MaxPlayers:  4,

		StartingBudget: defaultStartingBudget(),
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

// Validate reports the first out-of-range setting. Zero is not treated as
// "unset" here: a caller that wants defaults asks for DefaultSettings.
func (s Settings) Validate() error {
	switch {
	case s.DurationSec < minDurationSec || s.DurationSec > maxDurationSec:
		return fmt.Errorf("duration_sec must be %d..%d, got %d", minDurationSec, maxDurationSec, s.DurationSec)
	case !(s.Richness >= minRichness) || s.Richness > maxRichness: // the negation also rejects NaN
		return fmt.Errorf("richness must be %g..%g, got %g", minRichness, maxRichness, s.Richness)
	case !(s.SpawnPerMin >= 0) || s.SpawnPerMin > maxSpawnPerMin:
		return fmt.Errorf("spawn_per_min must be 0..%d, got %g", maxSpawnPerMin, s.SpawnPerMin)
	case s.StartingBudget != 0 && (s.StartingBudget < minStartingBudget || s.StartingBudget > maxStartingBudget):
		// Zero alone is exempt: it means "unset", and budget() reads it as the
		// default. Anything else the client sends is held to the range.
		return fmt.Errorf("starting_budget must be %d..%d, got %d", minStartingBudget, maxStartingBudget, s.StartingBudget)
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
		Width:          arenaWidth,
		Height:         arenaHeight,
		Colonies:       colonies,
		BarrierDensity: barrierDensity,
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
