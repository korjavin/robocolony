package lobby

import (
	"math"
	"testing"
)

// presets are the four the lobby form offers (rc-79d). They are client-side
// sugar — nothing but the density is ever sent — but the labels promise a
// non-open share of about 3.9x the density, so the numbers behind them have to
// stay legal and stay put.
var presets = []struct {
	name    string
	density float64
}{
	{"sparse", 0.03},
	{"normal", 0.08},
	{"rocky", 0.12},
	{"badlands", 0.15},
}

func TestBarriersDefaultsWhenUnset(t *testing.T) {
	if got := (Settings{}).barriers(); got != defaultBarrierDensity {
		t.Errorf("unset barriers() = %g, want %g", got, defaultBarrierDensity)
	}
	if got := (Settings{BarrierDensity: 0.12}).barriers(); got != 0.12 {
		t.Errorf("barriers() = %g, want 0.12", got)
	}
}

// TestSettingsWithoutBarrierDensityReplaysDefault is the replay contract: a
// lobby row written before the field existed has no barrier_density key, and
// must still regenerate the arena it originally did. Decoded, not hand-built:
// the decode path is the one persist.go takes at startup.
func TestSettingsWithoutBarrierDensityReplaysDefault(t *testing.T) {
	const old = `{"duration_sec":600,"richness":0.02,"spawn_per_min":6,"max_players":4,"seed":42}`
	s, err := decodeSettings(old)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if s.BarrierDensity != 0 {
		t.Fatalf("missing key decoded as %g, want 0 (unset)", s.BarrierDensity)
	}
	if got := s.GenOpts(2).BarrierDensity; got != 0.08 {
		t.Errorf("pre-existing lobby generates at %g, want 0.08", got)
	}
}

func TestGenOptsForwardsBarrierDensity(t *testing.T) {
	for _, p := range presets {
		s := DefaultSettings()
		s.BarrierDensity = p.density
		if got := s.GenOpts(2).BarrierDensity; got != p.density {
			t.Errorf("%s: GenOpts BarrierDensity = %g, want %g", p.name, got, p.density)
		}
	}
}

func TestValidateBarrierDensity(t *testing.T) {
	cases := []struct {
		name    string
		density float64
		ok      bool
	}{
		{"unset", 0, true},
		{"floor", minBarrierDensity, true},
		{"ceiling", maxBarrierDensity, true},
		{"below floor", 0.005, false},
		{"above ceiling", 0.16, false},
		{"negative", -0.05, false},
		{"NaN", math.NaN(), false},
	}
	for _, p := range presets {
		cases = append(cases, struct {
			name    string
			density float64
			ok      bool
		}{"preset " + p.name, p.density, true})
	}
	for _, c := range cases {
		s := DefaultSettings()
		s.BarrierDensity = c.density
		err := s.Validate()
		if c.ok && err != nil {
			t.Errorf("%s (%g): %v", c.name, c.density, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s (%g): accepted, want rejected", c.name, c.density)
		}
	}
}
