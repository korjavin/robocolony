package sim

import "slices"

// The match event feed (rc-pt6.8): the things that happen during a tick which a
// player wants marked on a timeline — a robot lost, a robot built, a component
// banked, a base that has stalled.
//
// Events are OBSERVATION, not state. Nothing in the simulation reads one back,
// nothing branches on one, and no rng draw depends on one — so they are
// deliberately absent from StateHash, exactly like traces, the sampled history
// and IdleReason (docs/engineering-notes.md: "observation is not state and must
// stay out of the hash"). Two worlds with equal state hash equal whatever their
// event buffers hold, and a replay reproduces the events because it reproduces
// the ticks, not because anything about them is stored.
//
// The buffer here is one tick deep. The match-wide buffer belongs to the layer
// that owns the match (internal/lobby/events.go); this package keeps no history
// of anything.

// EventKind says what happened.
//
// The numbers are wire values: kinds are appended, never inserted, and
// eventKindNames must stay in enum order.
type EventKind uint8

const (
	// EventLoss is a robot destroyed. It is the only kind that carries an
	// attacker.
	EventLoss EventKind = iota
	// EventBuild is a base releasing a finished robot (design §5.2 step 7).
	EventBuild
	// EventDeposit is a component banked in the colony's own base.
	EventDeposit
	// EventIdle is a base entering design §5.2 step 3 — nothing approved is
	// covered by the inventory. Edge-triggered: it fires on the tick the base
	// stalls, not on every tick it stays stalled. Base.IdleReason says why.
	EventIdle
	eventKindCount
)

var eventKindNames = [eventKindCount]string{
	EventLoss:    "loss",
	EventBuild:   "build",
	EventDeposit: "deposit",
	EventIdle:    "idle",
}

func (k EventKind) String() string {
	if k >= eventKindCount {
		return "unknown"
	}
	return eventKindNames[k]
}

// Event is one thing that happened, stamped with the tick it happened during.
//
// Tick is the tick the world was standing on while it happened, which is one
// less than the Tick of the snapshot that shows the result — the same
// convention prog.Trace already uses for "tick the decision was taken".
//
// Robot and Blueprint describe the subject: the robot lost, built, or doing the
// depositing. EventIdle has neither — a base is not a robot — so Robot is 0,
// which is not a legal robot id (World.NextID pre-increments).
//
// Attacker and AttackerColony are meaningful on EventLoss and zero elsewhere.
// AttackerColony has no "none" value — colony 0 is a real colony — so read it
// only when Kind is EventLoss.
type Event struct {
	Tick      uint64
	Kind      EventKind
	Colony    ColonyID
	Robot     int
	Blueprint string

	Attacker       int
	AttackerColony ColonyID
}

// Events returns what happened during the last Step, oldest first. A copy: the
// buffer is reused every tick, and a caller holding the live slice would find
// it rewritten under them one tick later (the aliasing bug RobotView.Blueprint
// already paid for once).
func (w *World) Events() []Event { return slices.Clone(w.events) }

// emit records one event at the current tick. Caller fills everything but Tick.
func (w *World) emit(e Event) {
	e.Tick = w.Tick
	w.events = append(w.events, e)
}
