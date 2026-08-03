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

// The POC catalogue. Design §6 lists more (legs, anti-gravity, light/heavy
// armor, enemy-robot and enemy-base radars, cannon, autogun); E7.2 adds them
// as extra rows in catalogue below — no code outside this file changes.
const (
	VariantNone Variant = iota // "no component" / empty cargo slot
	Tracks
	MediumArmor
	Manipulator
	PartsRadar
	Laser
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
	{Variant: MediumArmor, Kind: KindArmor, Name: "medium armor", Mass: 40, Value: 40},
	{Variant: Manipulator, Kind: KindManipulator, Name: "manipulator", Mass: 10, Value: 20},
	{Variant: PartsRadar, Kind: KindRadar, Name: "parts radar", Mass: 8, Value: 25},
	{Variant: Laser, Kind: KindWeapon, Name: "laser", Mass: 15, Value: 35},
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
	case counts[KindWeapon] > 2:
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
