# Decision record

Every question `docs/design_v0.1.md` left open, and how it was answered. The
design doc describes the *concept*; this file records what was actually decided
and why, so nobody has to re-derive it from git history or guess from code.

Each entry says what was decided, when, and what evidence or reasoning drove it.
Where a decision was measured rather than argued, the numbers are here — an
unreproducible measurement is how one earlier finding went unchecked for two
epics.

## Design §12 — open decisions, all resolved 2026-08-03

| # | Question | Decision | Why |
|---|---|---|---|
| P0 | Rule action model | Memory writes and broadcasts are **zero-tick side effects**; evaluation continues past them. The first rule with a *primary* action ends the tick. | §10.8's own example is unimplementable otherwise. |
| P0 | Signal reach | **Fixed radius, ~half the board.** | Global made `COME_HERE` a free colony-wide rally with no counterplay. A radius makes scouting and positioning design problems. |
| P0 | Carrying capacity | **Exactly one component.** | Kept as-is. |
| P0 | Starting allocation | Budget is a **lobby setting**; **unspent becomes random base inventory**. | Rewards an efficient design instead of silently wasting the remainder. |
| P0 | Target selection | Nearest, ties broken by distance then entity id. | Determinism requires a total order. Richer filters remain unspecified. |
| P0 | Scoring | `fleet_value + 25% × base_inventory_value`. | Measured — see below. |
| P1 | Weapon modules | Two weapons allowed, identical or not; **first *ready* weapon in slot order fires**. | Implemented in E5.1; §6.3 was ambiguous, not contradictory. |
| P1 | World visibility | **Observer sees all loose components.** | Consistent with §4.3's no-fog-of-war-for-the-player. |
| P1 | Production timing | **Mass-dependent.** | A heavy design should cost tempo as well as parts. |
| P1 | Terrain balance | Full §3.1 matrix implemented; `speedScale` raised 12 → 24. | At 12, every chassis with speed ≥ 12 collapsed to 1 tick/cell, so the fast end of the range was invisible and locomotion choice barely mattered. |
| P1 | Diplomacy / friendly fire | **No alliances. No friendly fire.** All colonies always hostile. | Friendly fire punishes beginners hardest; alliances are a large feature touching lobby, scoring and the shared signal channel. |
| P1 | Match parameters | Duration, richness, spawn rate, max players, budget — all lobby settings, server-validated. | |
| P2 | AI profiles | Four: tutorial, peaceful, defensive, aggressive. A profile is a **blueprint set plus a program library**, never a privileged controller. | Measured ladder below. |
| P2 | Spectating / replay | Eliminated players get **spectate plus full trace inspection**, not continued editing. | The trace machinery already exists, so this is cheap. |

## Decisions the design doc did not ask, but the code forced

**A wiped colony is permanently dead.** §5.3 promises a zero-robot colony with
covering inventory rebuilds, and the code honours that — but a colony at zero
robots collects nothing, so whatever it lacked when it died it lacks forever.
Measured across 16 seeds in every configuration tried: "wiped" and "dead at end"
were always *the same seeds*. Decided: annihilation is elimination. §5.3's
wording overstates the escape hatch and should be corrected.

**Live match state survives a restart** via seed + ordered command log, not a
world snapshot. `World` keeps `rng`, `nextID` and `signals` unexported with no
accessor, and all three are in `StateHash` — so a snapshot cannot be written
from outside `internal/sim` without growing a serialisation surface over exactly
the fields it hides. A replay needs nothing that is not already exported.
Measured cost: 7.3 s to replay the worst case the settings allow, ~0.6 s for a
default match.

**Every non-tick mutation of a live world must go through `Match.Apply`**, or
the replay rebuilds a world in which it never happened. This is the invariant
the whole persistence design rests on.

**A colony's loadout is stored as a frozen snapshot** (parts list + rules), never
as library ids. Ids would let a mid-match library edit make a restart rebuild the
colony from rules the match never ran.

## Measurements behind the balance decisions

**Scoring** — 112 matches (16 seeds × 7 matchups × 6000 ticks), sweeping the
inventory weight. Colonies finish holding 2–4× more value in the base than in
robots, so at 100% weight an *annihilated* hoarder beats the hunter that killed
it 16:0; at 50% it still wins 4 of 16. At 25%, no annihilated colony outscored a
surviving one in any of the 112 matches. Destroyed-enemy-value was considered and
**rejected on the data** — aggression already wins on denial alone, so a kill
credit pays twice.

**AI ladder** — 16 seeds × 6000 ticks, each profile against the default kit.
Re-measured after the signal radius and mass-dependent build time landed; no
profile was retuned, so these are the effect of those two changes on the
profiles exactly as they were. `internal/lobby/ai.go` carries the same table in
a comment and is the copy to trust if these ever disagree.

| profile | wipes the human | earliest wipe | avg wipe tick |
|---|---|---|---|
| tutorial | 0/16 | — | — |
| peaceful | 0/16 | — | — |
| defensive | 0/16 | — | — |
| aggressive | 10/16 | 999 | 2167 |

The radius is what softened the defensive rung: a spotter's `COME_HERE` no
longer reaches a responder in the far corner.

Tutorial and peaceful **cannot** kill: no blueprint in either carries a weapon,
pinned by a static test.

**Starter survivability** — the guard is deliberately crippled (autogun only, no
radar, no manipulator, no pursuit) and the weapon axis is deliberately *not*
fanned out. Measured overshoot: fanning it over all three weapons makes the
default beat the aggressive profile outright and drops a good custom design from
12/16 to 7/16. The default must lose to a thoughtful design — that is the game.

The harness is kept, not thrown away:
`ROBOCOLONY_BALANCE=1 go test ./internal/lobby/ -run TestBalance -v`.

**The rule language has `NOT`, which §10.2's EBNF does not** (rc-tad.12, 3 August
2026). One operand, JSON `{"op":"not","of":[…]}`, and the schema version stayed
at 1: `"v"` gates what a build will *read*, every stored program is still
readable, and bumping it would reject them all to announce an extension none of
them use. A program written *with* a NOT is not readable by an older build, but
one binary serves both the JS and the API, so that pairing cannot outlive a
reload — the same call PR #48 made for a wire change.

The evaluator case is one line. The cost is that three static analyses walk
condition trees assuming positive polarity, and each answers differently under a
NOT:

- **`inert_start` / `reactive_start`** walks each rule with world-observable
  predicates optimistically *true*. Under a NOT the optimistic reading is the
  opposite one, because a clean start is precisely the state in which nothing is
  seen, heard or detected: `NOT sees_enemy_robot` is satisfied at spawn. The
  walk therefore tracks polarity and lets the clean-start view answer inside a
  negation. Forcing `true` regardless would report a working
  `sees_component AND NOT sees_enemy_robot` program as stuck. It stays an
  over-approximation — `p AND NOT p` reads as reactive rather than as the
  contradiction it is — which is the direction this analysis has always erred
  in: it under-warns rather than over-warns.
- **`dead_predicate`** (a predicate whose sensor the blueprint lacks, hence
  permanently false) becomes **`always_true_predicate`** under an odd number of
  NOTs: a rule that fires unconditionally, not one that never fires. Different
  shape, different code, different message — printing "always false" over a rule
  that runs every tick is worse than saying nothing.
- **`unreachable_rule` declines NOT**, exactly as it already declines `OR`. Set
  containment decides implication for conjunctions of *positive* literals only;
  once a literal can be negated it is general propositional implication. Reading
  `NOT p` as one more opaque key would be actively wrong — `p AND NOT p` would
  read as dominating `p` and mark a live rule dead. A missed warning costs a
  player nothing; a wrong one tells them working code is dead.

## Reflexes: the actions that need no rule

**Depositing at your own base is a reflex, not a rule** (rc-tad.13, 4 August
2026). §10.5 says "if no rule matches, the robot idles"; there is now exactly one
exception, and it is the answer to a complaint from playing the game — *"deposit
component at base is obvious, there is no other logical action, so it should be
without programming at all"*.

The axis is **which actions are decisions and which are reflexes**, and the test
applied to each one was: *is there a plausible program that wants the opposite?*

- `deposit_component_at_base`, at own base, carrying — **no**. A carried
  component is worth zero (`World.FleetValue` sums *installed* components, not
  cargo), capacity is exactly one, and the base is the only thing that can take
  it. Not depositing is never a strategy, it is a forgotten rule. **Reflex.**
- `pick_up_component`, in reach, hands empty — **yes**. A hunter that ignores
  loot and keeps its tick is a real design. **Stays a decision.**
- Obstacle handling, wandering, attacking, fleeing — **yes**, several sensible
  variants each (turn left, turn right, turn random, retreat, path around).
  **All stay decisions.**

So the list is one entry long, deliberately. A wrong reflex is far more expensive
than a missing one: it silently overrides intent, and the player has no syntax to
argue with it.

**Mechanism.** The reflex fires only on a tick the program was going to waste,
which is what keeps it from changing any existing program: nothing matched, or
the matched rule's action had no target, or the action was "go home" while
already home. Anything the program actually chose — a turn, an attack, a drop, a
`stop`, a move that goes somewhere — wins, so an explicit deposit rule is still
in charge. Three alternatives were considered:

1. **Fallback on no-match alone** was the obvious minimum and does not work:
   §10.7's rule 2 (`carrying_component → move_to_own_base`) matches at the base
   too, so the robot arrives home and stands there re-issuing a move to the cell
   it occupies. Nothing "fails to match", the fallback never runs, and the
   scavenger silently stops delivering. Hence the "already home" clause — measured,
   not assumed.
2. **Removing the action and always auto-depositing** overrides live programs and
   forecloses the mechanics `drop_component` exists for.
3. **Pre-filling the rule in the editor** leaves the ceremony on screen and still
   costs a rule slot, which is the complaint.

**Evidence it changed nothing.** `TestScavengerNeedsNoDepositRule` runs the §10.7
scavenger with and without its deposit rule in the same generated arena and
compares `World.StateHash` on every one of 3000 ticks. They are identical, so the
starter program shipped one rule shorter (`lobby.DefaultProgram`) without
retuning any balance measurement above.

The reflex reads only the robot's view, never `w.rng`, and the trace says
`at own base carrying a component: depositing is automatic` rather than `no rule
matched` — the match inspector shows that string to players. `inert_start` is
unaffected and was checked, not assumed: the reflex needs cargo, a freshly built
robot has empty hands, and cargo only ever arrives through `pick_up_component`,
which is a rule (`TestInertStartSurvivesTheReflex`).

## Corrections to the design doc's own examples

All three worked programs in §10 are subtly wrong as printed. They are still
useful — as teaching material, paired with the warnings that catch them.

- **§10.7 component scavenger** opens with a rule that is now ceremony: the
  deposit reflex above does it, so the shipped template is the same program minus
  rule 1. It also requires a **parts radar**. Rule 4 is
  `move_to_radar_target`, which validation *errors* on without one; and with the
  radar removed no rule reads vision, so the program degenerates into a blind
  random walk.
- **§10.8 memory-assisted scout** can never act from a clean start. Every rule
  needs cargo or Point 1, and the only rule that sets Point 1 requires already
  carrying something. The validator now reports this as `inert_start`.
- **§10.9 defensive responder** only reacts. It idles at spawn until the world
  provides a stimulus — legitimate, and reported as a `reactive_start` note
  rather than a warning.
