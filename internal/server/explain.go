package server

import (
	"encoding/json"
	"net/http"

	"github.com/korjavin/robocolony/internal/prog"
	"github.com/korjavin/robocolony/internal/sim"
)

// The condition-level trace on the wire, and the editor's shadow test.
//
// One shape serves both, because they are the same answer asked about two
// programs:
//
//   - GET .../trace carries it for the *installed* program, recorded on the
//     tick the evaluator decided, and only for a robot someone is watching. It
//     rides the existing poll rather than the tick frame: the frame is measured
//     at ~11 KB for a whole match and a truth table per robot per tick would
//     spend that on the one robot a player has selected.
//   - POST .../shadow answers it for a *draft* program the editor is holding,
//     against the live robot's perception right now. Server-side because
//     programs are compiled and validated server-side, so a client-side
//     evaluator would be a second implementation of the language and would
//     start disagreeing with the simulation on the first balance change.
//
// Neither mutates anything. The shadow test takes the match's read lock, never
// Match.Apply: it reads the robot's view — already a value copy that clones the
// blueprint's component slice — and evaluates against that. The draft is never
// installed, no runtime entry is created, nothing enters the command log or
// StateHash, and a match observed this way simulates identically to one that
// is not.

// Explanation is one evaluation: which rule won, and the condition-by-condition
// evidence behind every rule's verdict.
type Explanation struct {
	// Tick the evaluation reflects. On the trace channel this is the tick the
	// robot last *decided* on, which is older than the world's tick whenever
	// the robot is mid-move; on the shadow channel it is the current tick.
	Tick   uint64 `json:"tick"`
	Rule   int    `json:"rule"` // rule that took the tick, -1 when none did
	Action string `json:"action,omitempty"`
	Reason string `json:"reason,omitempty"`
	Idle   bool   `json:"idle,omitempty"`

	Rules      []RuleVerdict  `json:"rules"`      // one per rule, program order
	Conditions []ConditionRow `json:"conditions"` // whole catalogue, grouped in order
}

// RuleVerdict is one rule's outcome: "won", "ran" (side effects only, and
// evaluation continued), "shadowed" (would have matched, but ShadowedBy took
// the tick first) or "not_met".
type RuleVerdict struct {
	Rule    int    `json:"rule"`
	Verdict string `json:"verdict"`
	// ShadowedBy is -1 for every verdict but "shadowed", rather than being
	// omitted: rule 0 is a perfectly good shadowing rule and omitempty would
	// erase it.
	ShadowedBy int `json:"shadowed_by"`
}

// ConditionRow is one line of the sensor truth table: a predicate, the argument
// it was asked with, and what it was worth.
type ConditionRow struct {
	Pred  string `json:"pred"`
	Group string `json:"group"` // catalogue group: self, vision, radar, ...
	Arg   int    `json:"arg,omitempty"`

	True bool `json:"true"`
	// Unknown is the "·" of the design's truth table: nothing was asked, so
	// neither ✓ nor ✗ is honest. Either the predicate takes an argument no rule
	// parameterises, or — on the shadow channel — it reads a signal inbox that
	// cannot be observed from outside a tick.
	Unknown bool `json:"unknown,omitempty"`
	// Impossible marks a row the blueprint can never satisfy for want of the
	// hardware, so the UI greys it instead of showing a ✗ that reads as "not
	// right now". The catalogue already ships each predicate's Needs, so the
	// client could derive this; it is answered here so the two cannot drift.
	Impossible bool `json:"impossible,omitempty"`

	// Value is the number behind the truth value where one exists — health
	// percentage, sighting count, distance to the nearest target. A pointer
	// because 0 is a real count.
	Value *int `json:"value,omitempty"`
}

// ShadowResult is a shadow test's answer. Robot is echoed so a client with
// several evaluations in flight can tell them apart.
type ShadowResult struct {
	Robot int `json:"robot"`
	Explanation
}

func explanation(ex prog.Explanation) Explanation {
	out := Explanation{
		Tick: ex.Tick, Rule: ex.Rule, Action: string(ex.Action),
		Reason: ex.Reason, Idle: ex.Idle,
		Rules:      make([]RuleVerdict, 0, len(ex.Rules)),
		Conditions: make([]ConditionRow, 0, len(ex.Conditions)),
	}
	for _, rv := range ex.Rules {
		out.Rules = append(out.Rules, RuleVerdict{
			Rule: rv.Rule, Verdict: string(rv.Verdict), ShadowedBy: rv.ShadowedBy,
		})
	}
	for _, c := range ex.Conditions {
		row := ConditionRow{
			Pred: string(c.Pred), Group: c.Group, Arg: c.Arg,
			True: c.True, Unknown: c.Unknown, Impossible: c.Impossible,
		}
		if c.HasValue {
			v := c.Value
			row.Value = &v
		}
		out.Conditions = append(out.Conditions, row)
	}
	return out
}

// ShadowTest evaluates a draft program against one live robot's perception this
// tick and reports which rule would win, without installing anything.
//
// No colony check, exactly as the trace endpoint has none: design §4.3 gives
// the observer no fog of war and the stream already shows every colony's robots
// to every session, so this reveals nothing a client could not already derive.
//
// No rate limit either, unlike the dry run next door: that one simulates 200
// ticks of a generated arena, this one walks a rule list once.
func (h *Robots) ShadowTest(matchID int64, robotID int, raw json.RawMessage) (ShadowResult, error) {
	m, ok := h.reg.Get(matchID)
	if !ok {
		return ShadowResult{}, errf(http.StatusNotFound, "match not found")
	}
	p, res, ok := parseProgram(raw)
	if !ok {
		// Not validationError: nothing has been compared against a blueprint
		// yet, and telling the player their program does not fit this robot
		// when it does not parse at all would send them looking in the wrong
		// place.
		return ShadowResult{}, cmdError{
			code:   http.StatusBadRequest,
			msg:    "the draft program does not load",
			issues: res.Errors,
		}
	}

	out := ShadowResult{Robot: robotID}
	var fail error
	// Read, not Apply: evaluating a draft is observation. A finished match is
	// allowed — its world is frozen, which makes it a perfectly good thing to
	// ask questions of.
	m.Read(func(w *sim.World, _ *prog.Runtime) {
		r := w.RobotByID(robotID)
		if r == nil {
			fail = errf(http.StatusNotFound, "no robot %d in this match", robotID)
			return
		}
		// The same validation an install would do. Reporting verdicts for a
		// program the player cannot actually install would be answering the
		// wrong question.
		if vres := prog.Validate(p, r.Blueprint); !vres.OK() {
			fail = validationError(vres)
			return
		}
		out.Explanation = explanation(prog.Explain(p, w.View(r, nil)))
	})
	if fail != nil {
		return ShadowResult{}, fail
	}
	markSignalsUnknown(&out.Explanation)
	return out, nil
}

// markSignalsUnknown is the one honest caveat of a shadow evaluation. A tick's
// signals are delivered into sim.World.Step and consumed inside it, so from out
// here there is no inbox to evaluate against: every received_* predicate would
// read false. False is a claim, and the wrong one — the robot may well be
// hearing something on this very tick — so those rows are reported as unknown
// and the truth table shows "·" for them.
//
// The recorded trace has no such gap: it is taken inside Decide, where the
// inbox is right there.
func markSignalsUnknown(ex *Explanation) {
	for i := range ex.Conditions {
		if ex.Conditions[i].Group == prog.GroupCommunication {
			ex.Conditions[i].Unknown, ex.Conditions[i].True = true, false
		}
	}
}

func (h *Robots) handleShadow(w http.ResponseWriter, r *http.Request) {
	_, matchID, robotID, err := commandTarget(r)
	if err != nil {
		writeCmdErr(w, r, err)
		return
	}
	var body struct {
		Program json.RawMessage `json:"program"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeCmdErr(w, r, errf(http.StatusBadRequest, "%s", err))
		return
	}
	out, err := h.ShadowTest(matchID, robotID, body.Program)
	if err != nil {
		writeCmdErr(w, r, err)
		return
	}
	writeCmdJSON(w, http.StatusOK, out)
}
