package prog

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/korjavin/robocolony/internal/sim"
)

// Severity separates what blocks a save from what merely deserves a yellow
// badge in the editor (design §10.10).
type Severity string

const (
	SevError   Severity = "error"
	SevWarning Severity = "warning"
	// SevNote is neither a fault nor a fix: something worth knowing about a
	// program that is doing exactly what it says. It carries no badge.
	SevNote Severity = "note"
)

// Issue is one validation finding, serializable to the editor.
type Issue struct {
	Severity Severity `json:"severity"`
	Code     string   `json:"code"` // stable machine code; Message is for humans
	Rule     int      `json:"rule"` // 0-based rule index, -1 for program level
	Message  string   `json:"message"`
}

// Result keeps errors and warnings apart on purpose: warnings must never block
// saving a program.
type Result struct {
	Errors   []Issue `json:"errors"`
	Warnings []Issue `json:"warnings"`
	// Notes are observations about a correct program. They are a separate
	// bucket rather than a third severity inside Warnings so that an editor
	// showing a badge per warning does not badge them: a badge on two of the
	// three programs we ship teaches players to ignore badges (rc-tad.5).
	Notes []Issue `json:"notes,omitempty"`
}

// OK reports whether the program may be saved and run.
func (r Result) OK() bool { return len(r.Errors) == 0 }

func (r *Result) err(rule int, code, format string, a ...any) {
	r.Errors = append(r.Errors, Issue{SevError, code, rule, fmt.Sprintf(format, a...)})
}

func (r *Result) warn(rule int, code, format string, a ...any) {
	r.Warnings = append(r.Warnings, Issue{SevWarning, code, rule, fmt.Sprintf(format, a...)})
}

func (r *Result) note(rule int, code, format string, a ...any) {
	r.Notes = append(r.Notes, Issue{SevNote, code, rule, fmt.Sprintf(format, a...)})
}

// Validate checks a program against the blueprint it will run on.
//
// Errors: anything structurally broken (see Decode), a rule with more than one
// primary action, and an action whose hardware the blueprint lacks — a radar
// action without a radar, pick_up_component without a manipulator (§10.10).
//
// Warnings: legal but ineffective. A predicate that can never be true because
// the sensor is absent, a robot whose only movement action is move_forward, a
// rule an earlier rule always pre-empts, an empty program.
//
// Notes: correct but worth knowing — a program that does nothing until the
// world gives it something to react to (see warnInertStart).
func Validate(p Program, b sim.Blueprint) Result {
	var r Result
	r.Errors = append(r.Errors, structure(p)...)

	if len(p.Rules) == 0 {
		r.warn(-1, "empty_program", "program has no rules; the robot will idle")
	}

	for i, rule := range p.Rules {
		primaries := 0
		for _, a := range rule.Then {
			spec, ok := LookupAction(a.Do)
			if !ok {
				continue // already reported by structure
			}
			if spec.Primary {
				primaries++
			}
			if lack := missing(spec.Needs, b); len(lack) > 0 {
				r.err(i, "missing_component", "rule %s: action %s needs %s, the blueprint has none",
					ordinal(i), a.Do, strings.Join(lack, " and "))
			}
		}
		if primaries > 1 {
			r.err(i, "multiple_primary", "rule %s has %d primary actions, at most one is allowed",
				ordinal(i), primaries)
		}
		warnDeadPredicates(&r, i, rule.When, b, 0)
	}

	warnForwardOnly(&r, p)
	warnDominated(&r, p)
	warnInertStart(&r, p, b)
	return r
}

// cleanStart is the view a freshly built robot gets on its first tick: standing
// on its own base at full health, hands empty, all three memory points empty,
// nothing seen, nothing on radar, nothing heard. Every zero value of RobotView
// is already the right answer, so only what is *not* zero is set here.
func cleanStart(b sim.Blueprint) sim.RobotView {
	return sim.RobotView{
		Health:    sim.StartingHealth(b),
		Cargo:     sim.VariantNone,
		Blueprint: b,
		HasBase:   true,
		AtBase:    true,
		// Weapons start loaded. Range stays zero, which is harmless: every
		// in-weapon-range predicate also needs a target, and there is none.
		WeaponReady: b.Has(sim.KindWeapon),
	}
}

// warnInertStart reports a program in which no rule at all matches a robot that
// has just been built, and separates the two very different reasons for it.
//
//   - Reactive (design §10.9): some rule needs nothing but a world event —
//     something seen, heard, detected or bumped into. The robot idles at the
//     start and then works. That is a legitimate design, so it is a note.
//   - Stuck (design §10.8 as printed): no rule can be reached by any world
//     event, because every one of them also waits on self-state — a memory
//     point, cargo — that the program never establishes. Nothing outside the
//     robot unblocks it, so this is the warning.
//
// The second walk is the whole mechanism: re-run the rule with every
// world-observable predicate optimistically true and see whether it matches. It
// is a per-predicate classification (PredicateSpec.World), not a search over
// rule interactions — general reachability analysis stays declined here for the
// same reason unreachable_rule declines it. Neither outcome is ever an error: a
// fragment meant to be combined with another program is legal to save, and may
// well be installed on a robot that is already carrying something.
func warnInertStart(r *Result, p Program, b sim.Blueprint) {
	if len(p.Rules) == 0 {
		return // empty_program already said it, and said it better
	}
	v := cleanStart(b)
	reactive := false
	for i := range p.Rules {
		m := matcher{v: v}
		if m.cond(p.Rules[i].When, 0) {
			return // something matches right now: not inert at all
		}
		if !reactive {
			w := matcher{v: v, anyWorld: true}
			reactive = w.cond(p.Rules[i].When, 0)
		}
	}
	if reactive {
		r.note(-1, "reactive_start", "no rule matches a freshly built robot, but a rule is waiting on "+
			"something the robot can see, hear or detect: it will idle until that happens, then act")
		return
	}
	r.warn(-1, "inert_start", "no rule matches a freshly built robot — empty memory, empty hands, "+
		"nothing in sight — and every rule also waits on something about the robot itself that no rule "+
		"ever sets, so nothing in the world around it can start it")
}

// warnDeadPredicates flags predicates whose sensor the blueprint lacks. Legal,
// just permanently false, so it is a warning.
func warnDeadPredicates(r *Result, rule int, c Condition, b sim.Blueprint, depth int) {
	if depth >= MaxCondDepth {
		return
	}
	switch c.Op {
	case OpPred:
		spec, ok := LookupPredicate(c.Pred)
		if !ok {
			return
		}
		if lack := missing(spec.Needs, b); len(lack) > 0 {
			r.warn(rule, "dead_predicate", "rule %s: %s is always false, the blueprint has no %s",
				ordinal(rule), c.Pred, strings.Join(lack, " or "))
		}
	case OpAnd, OpOr:
		for _, k := range c.Of {
			warnDeadPredicates(r, rule, k, b, depth+1)
		}
	}
}

// warnForwardOnly flags a robot that can drive but never change direction: it
// will jam against the first obstacle (design §10.10's own example).
func warnForwardOnly(r *Result, p Program) {
	onlyForward := false
	for _, rule := range p.Rules {
		for _, a := range rule.Then {
			spec, ok := LookupAction(a.Do)
			if !ok || (spec.Group != GroupMovement && spec.Group != GroupNavigation) {
				continue
			}
			if a.Do != MoveForward {
				return
			}
			onlyForward = true
		}
	}
	if onlyForward {
		r.warn(-1, "forward_only", "move_forward is the only movement action; the robot cannot turn or navigate")
	}
}

// warnDominated flags a rule an earlier rule always pre-empts. Sound but
// deliberately incomplete: it only recognises conjunction containment, which
// is the case editors actually produce (rule 5 = A AND B under rule 2 = A).
// Anything containing an OR is skipped rather than guessed at.
//
// Only a rule with a primary action can dominate: under the locked action
// model a side-effect-only rule fires and evaluation continues down the list
// (design §10.8 rule 1 is exactly that), so it pre-empts nothing.
func warnDominated(r *Result, p Program) {
	keys := make([][]string, len(p.Rules))
	for i, rule := range p.Rules {
		if !hasPrimary(rule) {
			continue
		}
		keys[i], _ = conjuncts(rule.When, 0)
	}
	for j := 1; j < len(p.Rules); j++ {
		kj, ok := conjuncts(p.Rules[j].When, 0)
		if !ok {
			continue
		}
		have := make(map[string]bool, len(kj))
		for _, k := range kj {
			have[k] = true
		}
		for i := 0; i < j; i++ {
			if keys[i] == nil || !subset(keys[i], have) {
				continue
			}
			r.warn(j, "unreachable_rule", "rule %s can never run: rule %s always matches first",
				ordinal(j), ordinal(i))
			break
		}
	}
}

func hasPrimary(r Rule) bool {
	for _, a := range r.Then {
		if a.Primary() {
			return true
		}
	}
	return false
}

func subset(want []string, have map[string]bool) bool {
	for _, k := range want {
		if !have[k] {
			return false
		}
	}
	return true
}

// conjuncts flattens nested ANDs into predicate keys. It returns nil for any
// tree containing an OR or an unknown node: implication is only decidable
// cheaply for pure conjunctions.
func conjuncts(c Condition, depth int) ([]string, bool) {
	if depth >= MaxCondDepth {
		return nil, false
	}
	switch c.Op {
	case OpPred:
		return []string{string(c.Pred) + ":" + strconv.Itoa(c.Arg)}, true
	case OpAnd:
		var out []string
		for _, k := range c.Of {
			ks, ok := conjuncts(k, depth+1)
			if !ok {
				return nil, false
			}
			out = append(out, ks...)
		}
		if len(out) == 0 {
			return nil, false
		}
		return out, true
	}
	return nil, false
}
