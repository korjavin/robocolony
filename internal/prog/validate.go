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
}

// OK reports whether the program may be saved and run.
func (r Result) OK() bool { return len(r.Errors) == 0 }

func (r *Result) err(rule int, code, format string, a ...any) {
	r.Errors = append(r.Errors, Issue{SevError, code, rule, fmt.Sprintf(format, a...)})
}

func (r *Result) warn(rule int, code, format string, a ...any) {
	r.Warnings = append(r.Warnings, Issue{SevWarning, code, rule, fmt.Sprintf(format, a...)})
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
	return r
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
