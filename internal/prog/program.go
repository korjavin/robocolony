// Package prog is the robot program model: the rule/condition/action data
// structure players author in the editor, its JSON wire format, and
// component-aware validation against a blueprint.
//
// It depends on internal/sim types only — no net/http, no database/sql.
// Programs are untrusted user input that reaches the simulation, so every
// entry point here fails cleanly (design §10.5 "safe failure") and never
// panics: unknown identifiers, malformed nesting and absurd sizes are errors.
//
// Evaluation lives in E3.2; this package only models and checks.
package prog

import (
	"encoding/json"
	"errors"
	"fmt"
)

// SchemaVersion is the "v" field of the JSON wire format. Bump it only for a
// breaking change; Decode rejects versions it does not know.
const SchemaVersion = 1

// Limits on untrusted input. Generous for a hand-authored program, small
// enough that a hostile one cannot exhaust the server.
const (
	MaxRules          = 256
	MaxActionsPerRule = 8
	MaxCondDepth      = 16
)

// PredicateID and ActionID are catalogue identifiers (design §10.3, §10.4).
type (
	PredicateID string
	ActionID    string
)

// CondOp tags a node of the condition tree (design §10.2 EBNF). Grouping is
// explicit: nesting is the only precedence rule.
type CondOp string

const (
	OpPred CondOp = "pred"
	OpAnd  CondOp = "and"
	OpOr   CondOp = "or"
)

// Condition is a predicate leaf or an AND/OR group.
type Condition struct {
	Op   CondOp      `json:"op"`
	Pred PredicateID `json:"pred,omitempty"` // OpPred only
	Arg  int         `json:"arg,omitempty"`  // predicate parameter, see ArgKind
	Of   []Condition `json:"of,omitempty"`   // OpAnd / OpOr operands
}

// Action is one catalogue action plus its optional argument.
type Action struct {
	Do  ActionID `json:"do"`
	Arg int      `json:"arg,omitempty"`
}

// Rule is WHEN condition THEN actions. Per the locked rule action model
// (AGENTS.md) a rule holds at most one primary action; any number of side
// effects (memory writes, broadcasts) may precede it.
type Rule struct {
	When Condition `json:"when"`
	Then []Action  `json:"then"`
}

// Program is an ordered rule list, evaluated top to bottom (design §10.2).
type Program struct {
	V     int    `json:"v"`
	Name  string `json:"name,omitempty"`
	Rules []Rule `json:"rules"`
}

// Condition and action constructors, for tests, fixtures and E3.2.
func Pred(id PredicateID) Condition { return Condition{Op: OpPred, Pred: id} }
func PredArg(id PredicateID, arg int) Condition {
	return Condition{Op: OpPred, Pred: id, Arg: arg}
}
func And(of ...Condition) Condition     { return Condition{Op: OpAnd, Of: of} }
func Or(of ...Condition) Condition      { return Condition{Op: OpOr, Of: of} }
func Do(id ActionID) Action             { return Action{Do: id} }
func DoArg(id ActionID, arg int) Action { return Action{Do: id, Arg: arg} }

// Primary reports whether the action ends the tick. Unknown actions are not
// primary; Validate reports them as errors.
func (a Action) Primary() bool {
	s, ok := LookupAction(a.Do)
	return ok && s.Primary
}

// Encode writes the JSON wire format, always stamping the current schema
// version.
func (p Program) Encode() ([]byte, error) {
	p.V = SchemaVersion
	return json.Marshal(p)
}

// Decode parses the JSON wire format. It rejects unknown schema versions and
// any structurally invalid program (unknown identifiers, out-of-range
// arguments, empty groups, over-deep nesting) with an error, never a panic.
//
// Decode deliberately does not need a blueprint: component-aware checks are
// Validate's job, and a program must be loadable to be shown to the player
// even when it does not fit the robot it is installed on.
func Decode(data []byte) (Program, error) {
	// Via a pointer so that a bare JSON "null" is rejected rather than read as
	// an empty program.
	var ptr *Program
	if err := json.Unmarshal(data, &ptr); err != nil {
		return Program{}, fmt.Errorf("prog: decode: %w", err)
	}
	if ptr == nil {
		return Program{}, errors.New("prog: decode: null program")
	}
	p := *ptr
	switch p.V {
	case 0:
		p.V = SchemaVersion // "v" omitted: assume the only version there is
	case SchemaVersion:
	default:
		return Program{}, fmt.Errorf("prog: unsupported schema version %d, want %d", p.V, SchemaVersion)
	}
	if issues := structure(p); len(issues) > 0 {
		errs := make([]error, len(issues))
		for i, is := range issues {
			errs[i] = errors.New(is.Message)
		}
		return Program{}, fmt.Errorf("prog: decode: %w", errors.Join(errs...))
	}
	return p, nil
}

// structure reports the errors that make a program un-representable, with no
// reference to any blueprint. Decode and Validate share it.
func structure(p Program) []Issue {
	var out []Issue
	add := func(rule int, code, format string, a ...any) {
		out = append(out, Issue{SevError, code, rule, fmt.Sprintf(format, a...)})
	}
	if len(p.Rules) > MaxRules {
		add(-1, "too_many_rules", "program has %d rules, limit is %d", len(p.Rules), MaxRules)
		return out
	}
	for i, r := range p.Rules {
		checkCond(&out, i, r.When, 0)
		switch {
		case len(r.Then) == 0:
			add(i, "no_action", "rule %s has no action", ordinal(i))
		case len(r.Then) > MaxActionsPerRule:
			add(i, "too_many_actions", "rule %s has %d actions, limit is %d", ordinal(i), len(r.Then), MaxActionsPerRule)
		}
		for _, a := range r.Then {
			spec, ok := LookupAction(a.Do)
			if !ok {
				add(i, "unknown_action", "rule %s: unknown action %q", ordinal(i), a.Do)
				continue
			}
			if msg := spec.Arg.check(a.Arg); msg != "" {
				add(i, "bad_argument", "rule %s: action %s %s", ordinal(i), a.Do, msg)
			}
		}
	}
	return out
}

// checkCond walks a condition tree, bailing out at MaxCondDepth so that a
// hostile deeply-nested program cannot blow the stack here.
func checkCond(out *[]Issue, rule int, c Condition, depth int) {
	add := func(code, format string, a ...any) {
		*out = append(*out, Issue{SevError, code, rule, fmt.Sprintf(format, a...)})
	}
	if depth >= MaxCondDepth {
		add("too_deep", "rule %s: condition nested deeper than %d levels", ordinal(rule), MaxCondDepth)
		return
	}
	switch c.Op {
	case OpPred:
		spec, ok := LookupPredicate(c.Pred)
		if !ok {
			add("unknown_predicate", "rule %s: unknown predicate %q", ordinal(rule), c.Pred)
			return
		}
		if msg := spec.Arg.check(c.Arg); msg != "" {
			add("bad_argument", "rule %s: predicate %s %s", ordinal(rule), c.Pred, msg)
		}
	case OpAnd, OpOr:
		if len(c.Of) == 0 {
			add("empty_group", "rule %s: empty %s group", ordinal(rule), c.Op)
			return
		}
		for _, k := range c.Of {
			checkCond(out, rule, k, depth+1)
		}
	default:
		add("unknown_op", "rule %s: unknown condition operator %q", ordinal(rule), c.Op)
	}
}
