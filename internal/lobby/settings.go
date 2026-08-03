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
}

// Legal ranges. Wide enough to be worth tuning, narrow enough that no setting
// can turn one match into a denial of service for the whole server.
const (
	minDurationSec, maxDurationSec = 60, 7200
	minRichness, maxRichness       = 0.001, 0.25
	maxSpawnPerMin                 = 120
	minPlayers, maxPlayers         = 1, 8

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
	}
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
	case s.MaxPlayers < minPlayers || s.MaxPlayers > maxPlayers:
		return fmt.Errorf("max_players must be %d..%d, got %d", minPlayers, maxPlayers, s.MaxPlayers)
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
