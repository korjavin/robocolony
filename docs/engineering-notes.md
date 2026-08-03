# Engineering notes

Traps this codebase has actually sprung, with the evidence. `AGENTS.md` states
the rules; this file explains why they exist, because a rule whose cost is
invisible gets "simplified away" by the next person.

Every entry here cost real debugging time at least once.

## The determinism guard has holes, and they are not obvious

`internal/sim` is deterministic given (seed, inputs), and `TestDeterminism` plus
`TestStateHashCoversState` guard it. **Four separate changes have found holes in
that guard**, each time by deliberately breaking determinism and discovering the
test still passed:

- A tick-loop test approved only **one** blueprint per base, so `Intn(1)`
  returned 0 regardless of which rand source it came from — a global-`math/rand`
  break passed clean. Fixed by approving two buildable blueprints.
- `StateHash` omitted `nextID`. Two worlds that allocated a different number of
  ids hand out different ids next, yet hashed equal.
- `RobotView.Blueprint.Components` aliased live world state, so a controller
  could write through the view and silently change a robot's mass and every
  subsequent hash.

**Therefore:** when you touch `internal/sim`, do the deliberate break. Swap one
roll to package-level `math/rand`, confirm the guard *fails*, restore it, and
say so in the PR. If the break passes, your test is too weak and strengthening
it is part of the work — that is not optional politeness, it is the only thing
that has ever caught these.

Corollary: any new mutable field needs a `StateHash` entry **and** a
`TestStateHashCoversState` case. Observation (traces, history, idle reasons) is
not state and must stay out of the hash.

## Fanning out over one axis leaves the next axis binding

The starter kit approves blueprints so a colony can keep building as its stock
changes. This has been fixed **three times, on three different axes**:

1. Bases stalled holding unusable salvage — the only blueprint pinned an exact
   **armor** variant. Fixed by fanning over armor.
2. New locomotion variants shipped; a colony holding legs and no tracks stalled
   identically. Fixed by fanning over locomotion × armor.
3. A colony still stalled at tick 1000 holding tracks, medium armor and a
   manipulator — every blueprint demanded a **parts radar**. Nobody had fanned
   the radar row.

The manipulator is currently single-variant and is the next candidate.

**Therefore:** when a required component kind gains variants, check the starter
fan-out covers it. And when you measure "the colony never recovered", check
whether it is *rebuilding badly* or *not building at all* — those look identical
from outside and have opposite fixes. The third instance was found only because
someone printed the base's idle reason instead of trusting the earlier diagnosis.

## `#inspector` is cleared every tick — nothing interactive may live in it

`renderInspector()` calls `replaceChildren()` at 10 Hz. Building a control
**once** and updating it in place is not sufficient protection: clearing the
parent *detaches* the cached node and re-appends it, and a detached `<select>`
loses an open dropdown. The program picker closed itself ~100 ms after a player
opened it, making installs impossible.

The roster and the trace history already have their own panels for this reason.
The command controls now do too.

**Therefore:** anything that can hold focus or an open popup lives in its own
panel that is never cleared. Verify with focus retention across a tick — it is
observable and shares the identical cause.

Related: a `<select>` rebuilt ten times a second also closes under the pointer,
so lists and pickers must be built once and updated by writing text nodes, never
re-parsed. The roster proves this with a `MutationObserver` recording **0**
structural mutations over 40 ticks at 160 rows.

## Features ship unreachable unless someone gives them a door

Twice: the program editor existed for two epics with **no page linking to it**,
reachable only by typing the URL; and AI colonies merged with a complete API and
no lobby control, so solo play was invisible.

Both times each executor stayed correctly inside its file ownership, and the gap
was *between* the beads rather than inside any of them.

**Therefore:** a bead that adds a capability should name where the player reaches
it. If UI is out of scope, say so explicitly in the PR body so a follow-up gets
filed — do not assume it is obvious.

## Two green branches can be red together

Per-branch CI cannot see integration. A lobby test snapshotted robot positions
into a slice and compared by index; a simulation change shifted tick ordering so
production fired inside the test's window; `basis[i]` ran off the end and
panicked. Both branches were green alone.

Separately, two executors independently defined package-level `errf` and
`validationError` in `internal/server` with *different* semantics, plus a
duplicate test helper. Nothing conflicted textually — it simply did not compile
once merged.

**Therefore:** `w.Robots` is not a stable index space (production appends,
destruction removes) — key by entity id, never position. And when several
executors work one package in parallel, expect name collisions git cannot show
you; prefer distinct, domain-prefixed names for anything package-level.

## Replay: store what happened, not what to look up

Match persistence replays from seed + command log. Two consequences that are
easy to get wrong:

- A colony's loadout is stored as a **frozen snapshot** of parts and rules. If
  it stored library ids, editing your library mid-match would make a restart
  rebuild the colony from rules the match never ran.
- **Every non-tick mutation must go through `Match.Apply`.** A mutation applied
  directly to the world is invisible to the log, so the replay rebuilds a world
  where it never happened.

`fingerprint()` is *computed* — the state hash of a fixed mini-match plus the
component catalogue — so a balance change invalidates stale logs automatically
rather than depending on someone remembering to bump a constant. When a change
moves it, in-flight logs are refused and those matches finished: that is the
loud, safe outcome and should be stated in the PR, not engineered around.

## Shutdown, SSE, and the bug behind the bug

`http.Server.Shutdown` waits for active requests, and an SSE stream is an active
request that never finishes — so the drain could only time out: 30 s, then a
non-zero exit a container runtime reads as a crash.

The tempting fix, a `WriteTimeout`, is wrong: it severs long-lived streams
mid-flight, which is why it was removed in the first place.

Worse, the drain error was returned *immediately*, which skipped the tick
drivers' own shutdown — so every deploy with a spectator attached both hung for
30 s **and** destroyed the in-flight match records persistence existed to save.
Two features silently cancelling each other.

**Therefore:** `RegisterOnShutdown` signals the streams; ordinary requests still
drain normally; the drain error is logged, the rest of shutdown still runs, and
the error is reported at the end.

## Balance claims need numbers, and the harness must survive

Every balance decision here was measured over 16 seeds, and more than once the
measurement contradicted the plausible story:

- "It never rebuilds" was assumed to mean "rebuilds into the enemy's guns". It
  actually meant the base stopped building entirely.
- Adding a flee rule to the starter *reduced* survival (avg 1080 → 996 ticks).
  The geometry is not tunable: weapon range exceeds vision range and the hunter
  is on the faster chassis.
- Fanning the base guard across all weapons was measured as an **overshoot**
  before it was rejected, not merely suspected.

**Therefore:** `ROBOCOLONY_BALANCE=1 go test ./internal/lobby/ -run TestBalance -v`
is kept in the repo. Reuse it; do not build a second harness. Report before/after
per profile. An unreproducible measurement is exactly how the first bad diagnosis
survived two epics.

## Verify the executor, not the report

A completion report is not evidence. Failure modes seen here: a branch reported
complete with zero commits; a claimed determinism break that had silently tested
*unmodified* source because the patch pattern did not match; a CI check reported
as passing that was reading a stale run.

**Therefore:** check the worktree and the diff. Gate merges on CI as a separate
step — chaining `check && merge` merges on red, and `go test | grep` returns
grep's exit status, not the test's.
