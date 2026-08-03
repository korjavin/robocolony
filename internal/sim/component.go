package sim

import (
	"errors"
	"fmt"
	"slices"
)

// ComponentKind is a component category (design §6.1, §6.2).
type ComponentKind uint8

const (
	KindLocomotion ComponentKind = iota
	KindArmor
	KindManipulator
	KindRadar
	KindWeapon
)

var kindNames = [...]string{"locomotion", "armor", "manipulator", "radar", "weapon"}

func (k ComponentKind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "unknown"
}

// Variant identifies one concrete component in the catalogue.
type Variant uint8

// The POC catalogue. Design §6 lists more (legs, anti-gravity, enemy-robot and
// enemy-base radars); E7.2 adds them as extra rows in catalogue below — no code
// outside this file changes.
//
// New variants are appended, never inserted: a stored blueprint holds these
// numbers, so shifting one silently rewrites every saved design.
const (
	VariantNone Variant = iota // "no component" / empty cargo slot
	Tracks
	MediumArmor
	Manipulator
	PartsRadar
	Laser
	LightArmor
	HeavyArmor
	Cannon
	AutoGun
)

// Component is a catalogue entry. Mass and Value are placeholder balance
// numbers; E7.2 tunes them and adds per-kind stats (speed, range, damage).
type Component struct {
	Variant Variant
	Kind    ComponentKind
	Name    string
	Mass    int
	Value   int
}

// catalogue is data, not a switch. Adding a component is adding a row.
var catalogue = []Component{
	{Variant: Tracks, Kind: KindLocomotion, Name: "tracks", Mass: 30, Value: 30},
	{Variant: LightArmor, Kind: KindArmor, Name: "light armor", Mass: 20, Value: 25},
	{Variant: MediumArmor, Kind: KindArmor, Name: "medium armor", Mass: 40, Value: 40},
	{Variant: HeavyArmor, Kind: KindArmor, Name: "heavy armor", Mass: 70, Value: 65},
	{Variant: Manipulator, Kind: KindManipulator, Name: "manipulator", Mass: 10, Value: 20},
	{Variant: PartsRadar, Kind: KindRadar, Name: "parts radar", Mass: 8, Value: 25},
	{Variant: Laser, Kind: KindWeapon, Name: "laser", Mass: 15, Value: 35},
	{Variant: Cannon, Kind: KindWeapon, Name: "projectile cannon", Mass: 35, Value: 55},
	{Variant: AutoGun, Kind: KindWeapon, Name: "automatic gun", Mass: 20, Value: 30},
}

// ---------------------------------------------------------------------------
// Combat balance. Every number in this block, plus the Mass and Value columns
// above, is a placeholder: nothing is balanced yet. E7.3 retunes the game by
// editing these tables, and E7.2 adds weapons and armor tiers by adding rows.
// No logic below reads a variant by name.
// ---------------------------------------------------------------------------

// MaxWeapons is the design §6.3 limit on weapon modules, and the number of
// independent weapon cooldowns a robot tracks.
const MaxWeapons = 2

// healthBase is the durability of the chassis itself, before armor.
const healthBase = 20

// salvageDropPercent is the chance, per *installed* component, that it survives
// the wreck as an ordinary loose component (design §8.2: a random subset drops,
// everything else disappears). Rolled once per component on the world's rng.
const salvageDropPercent = 40

// WeaponSpec is one row of the design §8.1 weapon table. Range is in Chebyshev
// cells, Cooldown in ticks between shots of that same module, Accuracy the
// percent chance to hit, rolled once per shot on the world's rng.
type WeaponSpec struct {
	Variant  Variant
	Range    int
	Damage   int
	Cooldown int
	Accuracy int
}

// weaponSpecs gives each weapon its design §8.1 identity: the laser is accurate
// and long-ranged, the cannon slow and heavy-hitting, the autogun fast and
// wild. Projectile travel time is not modelled — a shot resolves on the tick it
// is fired.
var weaponSpecs = []WeaponSpec{
	{Variant: Laser, Range: 8, Damage: 7, Cooldown: 6, Accuracy: 90},
	{Variant: Cannon, Range: 6, Damage: 26, Cooldown: 20, Accuracy: 60},
	{Variant: AutoGun, Range: 4, Damage: 4, Cooldown: 2, Accuracy: 45},
}

// ArmorSpec is one row of the design §6.1 armor table: durability bought with
// mass, and mass is what the §6.4 speed model taxes.
type ArmorSpec struct {
	Variant Variant
	Health  int
}

var armorSpecs = []ArmorSpec{
	{Variant: LightArmor, Health: 50},
	{Variant: MediumArmor, Health: 100},
	{Variant: HeavyArmor, Health: 180},
}

// WeaponStats returns the weapon row for a variant, or false when the variant
// is not a weapon the balance table knows about.
func WeaponStats(v Variant) (WeaponSpec, bool) {
	for _, s := range weaponSpecs {
		if s.Variant == v {
			return s, true
		}
	}
	return WeaponSpec{}, false
}

// armorHealth is the durability an armored body contributes. An armor variant
// with no row contributes nothing rather than guessing.
func armorHealth(v Variant) int {
	for _, s := range armorSpecs {
		if s.Variant == v {
			return s.Health
		}
	}
	return 0
}

// Weapons returns the blueprint's weapon variants in slot order. Slot order is
// installation order, and it is what picks the firing weapon (design §12 P1
// leaves selection open; E7.9 owns the real answer).
func (b Blueprint) Weapons() []Variant {
	var out []Variant
	for _, v := range b.Components {
		if k, ok := v.Kind(); ok && k == KindWeapon {
			out = append(out, v)
		}
	}
	return out
}

// Catalogue returns every buildable component.
func Catalogue() []Component { return slices.Clone(catalogue) }

// Lookup returns the catalogue entry for a variant.
func Lookup(v Variant) (Component, bool) {
	for _, c := range catalogue {
		if c.Variant == v {
			return c, true
		}
	}
	return Component{}, false
}

func (v Variant) String() string {
	if c, ok := Lookup(v); ok {
		return c.Name
	}
	if v == VariantNone {
		return "none"
	}
	return "unknown"
}

// Kind returns the variant's category, or false if it is not in the catalogue.
func (v Variant) Kind() (ComponentKind, bool) {
	c, ok := Lookup(v)
	return c.Kind, ok
}

// Blueprint is an approved physical configuration plus its default program
// (design §5.1). Treat an approved blueprint as immutable.
type Blueprint struct {
	ID         string
	Name       string
	Components []Variant // ordered; the build consumes exactly these
	ProgramID  string    // default program installed on production
}

// Configuration constraint violations from design §6.3.
var (
	ErrUnknownComponent = errors.New("unknown component variant")
	ErrLocomotion       = errors.New("blueprint needs exactly one locomotion unit")
	ErrArmor            = errors.New("blueprint needs exactly one armored body")
	ErrRadarLimit       = errors.New("blueprint may carry at most one radar")
	ErrWeaponLimit      = errors.New("blueprint may carry at most two weapons")
)

// Validate enforces design §6.3.
func (b Blueprint) Validate() error {
	var counts [len(kindNames)]int
	for _, v := range b.Components {
		k, ok := v.Kind()
		if !ok {
			return fmt.Errorf("%w: %d", ErrUnknownComponent, v)
		}
		counts[k]++
	}
	switch {
	case counts[KindLocomotion] != 1:
		return fmt.Errorf("%w, has %d", ErrLocomotion, counts[KindLocomotion])
	case counts[KindArmor] != 1:
		return fmt.Errorf("%w, has %d", ErrArmor, counts[KindArmor])
	case counts[KindRadar] > 1:
		return fmt.Errorf("%w, has %d", ErrRadarLimit, counts[KindRadar])
	case counts[KindWeapon] > MaxWeapons:
		return fmt.Errorf("%w, has %d", ErrWeaponLimit, counts[KindWeapon])
	}
	return nil
}

// Has reports whether the blueprint carries at least one component of a kind.
// A manipulator is required to collect or deliver components (design §6.3).
func (b Blueprint) Has(kind ComponentKind) bool {
	for _, v := range b.Components {
		if k, ok := v.Kind(); ok && k == kind {
			return true
		}
	}
	return false
}

// Mass is the total mass of the blueprint's components (design §6.4 input).
func (b Blueprint) Mass() int { return b.sum(func(c Component) int { return c.Mass }) }

// Value is the total component value of the blueprint.
func (b Blueprint) Value() int { return b.sum(func(c Component) int { return c.Value }) }

func (b Blueprint) sum(f func(Component) int) int {
	total := 0
	for _, v := range b.Components {
		if c, ok := Lookup(v); ok {
			total += f(c)
		}
	}
	return total
}
