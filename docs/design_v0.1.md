# Autonomous Robot Colony Simulation

*Game Design Document - working title, core game concept, and draft visual programming language*

**Status:** Concept specification

**Version:** 0.1

**Date:** 3 August 2026

**Audience:** Game designers, gameplay engineers, simulation engineers, UI/UX designers

> **Design intent:** The player is an engineer of autonomous behavior, not a unit commander. Success comes from robot design, production choices, and robust programs that continue to work while the player is offline.

# 1. Executive Summary

The game is a real-time, server-hosted simulation in which multiple autonomous robot colonies compete on a procedurally generated arena. Human players prepare robot blueprints and behavior programs, enter a lobby, and start the simulation simultaneously. AI-controlled colonies are also supported for solo play, training, or filling lobby slots.

Robots are never steered directly. Each robot executes an ordered list of priority rules, perceives the world through limited forward vision and optional specialist radars, and may exchange simple signals with friendly robots. Players may observe the complete simulation and adapt during a match by recalling individual robots to their base and installing a different program.

## 1.1 Core pillars

- **Autonomy first.** The colony must continue operating without continuous player attention.

- **Program behavior, not movement.** The player changes rules and configurations rather than issuing path-by-path commands.

- **Modular trade-offs.** Mobility, armor, sensors, manipulation, and weapons compete through cost and mass.

- **Emergent competition.** Colonies may focus on scavenging, avoidance, defense, aggression, or mixed strategies.

- **Readable simulation.** The match should be enjoyable to watch through a map view and clear statistics.

## 1.2 Explicit non-goals for the initial design

- No direct steering, aiming, or manual movement of robots.

- No energy, fuel, ammunition logistics, maintenance, or repair system.

- No destructible bases.

- No pre-match knowledge of the generated arena and no pre-authored geographic zones.

- No fog of war for the observing player.

# 2. Match Structure

## 2.1 Lobby and start flow

1.  Players join a lobby. The lobby may include real players and AI-controlled colonies.

2.  The lobby owner selects the match and resource-generation settings.

3.  Each human player selects or creates saved robot blueprints and programs from their personal libraries.

4.  Every player receives the same known starting budget and prepares an initial colony within that budget.

5.  The arena layout is unknown before the match starts.

6.  When the lobby owner starts the match, all colonies enter the arena simultaneously.

7.  No additional player may join after the simulation starts.

## 2.2 Continuous server simulation

The match runs in real time on the server. A colony remains active when its player disconnects or closes the game. An absent player loses only the ability to observe and dynamically reprogram robots; existing programs, production, movement, combat, and communication continue normally.

## 2.3 Match settings

| **Setting**          | **Current intent**                                                          |
|----------------------|-----------------------------------------------------------------------------|
| Match duration       | Fixed per lobby; exact supported values are TBD.                            |
| Initial map richness | Controls how many ready-made components are placed during arena generation. |
| Resource spawn rate  | Controls the slow appearance of additional components during the match.     |
| Map generation seed  | Randomized; exposure or manual selection is TBD.                            |
| Player / AI count    | Configurable; supported limits are TBD.                                     |

# 3. Arena and World Model

The arena is a generated two-dimensional or visually simplified navigable world containing player bases, terrain, impassable barriers, initial robot components, and later resource spawns. The exact rendering style and dimensionality are not yet specified, but gameplay logic assumes positions, headings, ranges, paths, and terrain traversal rules.

## 3.1 Terrain

| **Terrain class**              | **Legs**            | **Tracks**          | **Anti-gravity** |
|--------------------------------|---------------------|---------------------|------------------|
| Open / regular ground          | Passable            | Passable            | Passable         |
| Rubble or leg-favored obstacle | Passable or favored | Impassable          | Passable         |
| Track-favored obstacle         | Impassable          | Passable or favored | Passable         |
| Mountain ridge / hard barrier  | Impassable          | Impassable          | Impassable       |

The final terrain catalogue and traversal matrix require balancing. Anti-gravity movement is intended to cross almost all ordinary terrain but must still route around absolute barriers such as mountain ridges.

## 3.2 Components in the world

Resources are complete, ready-made robot components rather than raw materials. Examples include tracks, legs, armor bodies, manipulators, radars, and weapons. Components are scattered during map generation, with slow replenishment according to lobby settings. Resource scarcity is intentional and should force competition, exploration, recovery of battle debris, and production trade-offs.

# 4. Player Agency and Observation

## 4.1 Prohibited direct control

The player cannot click a destination, steer a robot, aim a weapon, manually collect a component, or operate the base moment by moment. Robot decisions must originate from installed programs, except for the special recall command described below.

## 4.2 Recall and reprogramming

1.  The player selects one specific robot and issues a Recall to Base signal.

2.  Recall is a system-level command: the robot suspends its installed behavior and navigates back autonomously.

3.  After the robot reaches the base, the player may install an existing program from the library or create a new one.

4.  Installing a program clears all three coordinate-memory cells.

5.  The robot leaves the base and starts the new program from a clean state.

> **Constraint:** Reprogramming is intentionally delayed by travel. A threatened, blocked, or very distant robot may fail to return before the situation changes.

## 4.3 Information available to the player

There is no fog of war for the human observer. During a match, the player can see the arena, all bases, and all robots from every colony, including their movement and combat. This omniscience does not transfer to robot programs: a robot may only use its own position, base position, local vision, installed radar, memory, and received signals.

Whether all loose components are globally visible to the player, or only components discovered by the player's colony, remains an open decision.

## 4.4 Required observation interface

- Arena map or mini-map with distinct colonies and robot archetypes.

- Selectable robot details: blueprint, installed program, health, carried component, current action, and memory points.

- Base inventory, approved blueprints, current build, and production progress.

- Colony statistics: active robots, fleet value, collected parts, losses, kills, and production history.

- Match clock and provisional leaderboard.

- Program editor and program library accessible during the match.

# 5. Base and Production

Each colony owns one permanent, invulnerable base. Robots deposit components there, new robots are assembled there, and recalled robots are reprogrammed there. Players do not directly operate production after the simulation begins; the base autonomously uses the blueprints approved for that match.

## 5.1 Approved blueprints

A blueprint defines a legal physical robot configuration and its default program. Before the match, the player approves one or more blueprints for automatic production. Blueprints and programs are distinct reusable assets: a physical blueprint may be associated with a default program, while an individual finished robot may later receive another program.

## 5.2 Automatic build algorithm

1.  Inspect the base inventory.

2.  Determine every approved blueprint whose complete component set is available.

3.  If none are buildable, wait for the inventory to change.

4.  Randomly select one buildable blueprint.

5.  Reserve and consume its required components.

6.  Assemble the robot over a short build time.

7.  Release it with the blueprint's default program.

Build time may be constant or depend on component count and mass; this remains TBD. The random selection rule is intentional for the current concept, although weighted priorities could be explored later.

## 5.3 Recovery after losses

The base cannot be destroyed. If a colony has no active robots but retains every component required by an approved blueprint, it can build a new robot and re-enter active play. A colony with no robots and insufficient inventory is effectively inactive because it has no unit able to retrieve more components, unless a future mechanic allows components to appear within collectible range of the base.

# 6. Robot Construction

Every robot is modular. The minimum valid robot contains one locomotion unit and one armored body. Every valid robot also includes a built-in minimal controller capable of running the visual rule program; processor chips are not separate components.

## 6.1 Required components

| **Category** | **Current variants**                | **Primary effect**                                                               |
|--------------|-------------------------------------|----------------------------------------------------------------------------------|
| Locomotion   | Legs; tracks; anti-gravity platform | Base speed, terrain access, and mass handling.                                   |
| Armored body | Three armor classes                 | Health / durability and mass. Exact light, medium, and heavy statistics are TBD. |
| Controller   | Built into every valid robot        | Runs the installed program; never scavenged or selected separately.              |

## 6.2 Optional functional components

| **Component**     | **Function**                                               | **Current limit**                                                            |
|-------------------|------------------------------------------------------------|------------------------------------------------------------------------------|
| Manipulator       | Picks up, carries, and deposits ready-made components.     | Presence is required for scavenging; exact slot and carrying limits are TBD. |
| Parts radar       | Detects loose robot components at range.                   | At most one radar of any type.                                               |
| Enemy robot radar | Detects hostile robots at range.                           | At most one radar of any type.                                               |
| Enemy base radar  | Detects opponent bases as navigation landmarks.            | At most one radar of any type.                                               |
| Laser weapon      | Accurate weapon; detailed range, rate, and damage are TBD. | Counts toward the two-weapon limit.                                          |
| Projectile cannon | Slow, heavy-hitting weapon; detailed ballistics are TBD.   | Counts toward the two-weapon limit.                                          |
| Automatic gun     | High rate of fire with low accuracy.                       | Counts toward the two-weapon limit.                                          |

## 6.3 Configuration constraints

- Exactly one locomotion unit is required.

- Exactly one armored body is required.

- At most one radar may be installed.

- At most two weapon modules may be installed.

- A manipulator is required to collect or deliver components.

- Additional components increase total mass and reduce effective speed.

The phrase “two types of weapons” is currently interpreted as two weapon modules, potentially identical or different. This interpretation must be confirmed during detailed design.

## 6.4 Speed model

```text
effective_speed = locomotion_base_speed
                - mass_penalty(total_robot_mass, locomotion_type)
                + terrain_modifier(current_terrain, locomotion_type)
```

No final formula or numeric values are specified. The required behavior is that heavier bodies and equipment reduce speed, while locomotion type controls both base speed and terrain access. Anti-gravity requires a balancing disadvantage such as higher component value, lower mass tolerance, or reduced durability.

# 7. Perception, Navigation, Memory, and Communication

## 7.1 Built-in forward vision

Every robot has short-range directional vision without installing a radar. It sees only in the direction it is facing, so a program must turn the robot to scan its surroundings. Turning and forward movement therefore remain meaningful program actions.

## 7.2 Specialist radars

A radar provides longer-range detection for exactly one target class: loose components, enemy robots, or enemy bases. A radar allows a program to select a detected target and move toward or away from it. Enemy bases are not attack objectives; they are strategic landmarks for avoidance, approach, orientation, or patrol.

## 7.3 Always-known coordinates

- The robot's current position.

- The position of its own base.

- The values stored in its three coordinate-memory cells.

The robot does not automatically know the map, terrain beyond its navigation context, loose component locations, hostile robot positions, or enemy base positions.

## 7.4 Coordinate memory

Each robot has exactly three coordinate registers: Point 1, Point 2, and Point 3. A point may be empty or hold one world coordinate. Rules may write the robot's current position, a visible target position, a radar target position, or a received signal position. Programs may test whether a point is set, move toward or away from it, and clear it.

## 7.5 Communication

Communication uses one shared friendly channel. Signals are events rather than commands and never interrupt a receiver automatically. A receiver reacts only if its installed program contains a matching rule.

| **Signal** | **Payload**        | **Intended use**                                                       |
|------------|--------------------|------------------------------------------------------------------------|
| COME_HERE  | Sender coordinates | Request nearby or connected friendly robots to converge on the sender. |
| AVOID_HERE | Sender coordinates | Warn friendly robots about the sender's current area.                  |

A signal exists only at the moment of transmission. A receiving robot may preserve its coordinate by writing it into Point 1, Point 2, or Point 3. Signal reception range is not yet fully resolved: the current concept requires a common channel heard by all eligible friendly robots, but whether eligibility is global or limited by radio distance remains TBD.

# 8. Combat and Destruction

Combat is autonomous. A robot attacks only when its program selects an enemy target and issues an attack action. Weapon type determines accuracy, rate of fire, range, and damage; armored body type determines durability. Exact combat formulas are intentionally deferred to balancing.

## 8.1 Weapon identities

| **Weapon**        | **Gameplay identity**                | **Balancing dimensions**                                   |
|-------------------|--------------------------------------|------------------------------------------------------------|
| Laser             | Accurate and potentially long-range. | Damage, fire rate, range, mass, component value.           |
| Projectile cannon | Slow, powerful shots.                | Burst damage, projectile travel, accuracy, cooldown, mass. |
| Automatic gun     | Fast fire with low accuracy.         | Rate, spread, damage per hit, range, mass.                 |

## 8.2 Destruction and salvage

When a robot is destroyed, a random subset of its physical components drops onto the arena. All remaining components disappear. Dropped modules become ordinary loose components and may be detected, collected, and delivered to any base by a robot with a manipulator.

> **Design effect:** Battlefields become temporary resource sites. Aggression can destroy an opponent's productive capacity, but combat may also provide salvage to the winner, a third party, or even the original owner.

# 9. Victory and Scoring

A match ends after a fixed simulation duration. The initial victory concept rewards survival and the strength of the remaining robot population. A simple robot count is vulnerable to last-minute production of tiny, low-value robots, so the provisional direction is component-weighted fleet value.

```text
colony_score = SUM(value_of_installed_components(robot))
               for every surviving robot at match end
```

This formula is not final. Candidate additions include a survival requirement, remaining base inventory, collected resources, destroyed enemy value, time active, or scenario-specific objectives. The first implementation may use weighted robot count while telemetry is collected for balancing.

# 10. Draft Visual Programming Language v0.1

## 10.1 Design goals

- Readable by non-programmers as a visual list of IF/THEN rules.

- Small enough to understand while watching a live simulation.

- Expressive enough for scavenging, exploration, combat, retreat, patrol, and cooperation.

- Deterministic and safe to execute on the server.

- Able to produce imperfect, surprising, or failing behavior without crashing the simulation.

## 10.2 Program structure

A program is an ordered list of rules. The robot evaluates rules from highest to lowest priority. Conditions may use AND and OR operators and explicit grouping. The first matching rule provides the action for the current evaluation cycle. On the next cycle, the list is evaluated again, allowing a higher-priority rule to pre-empt an ongoing lower-priority behavior.

```ebnf
PROGRAM   := ordered RULE list
RULE      := WHEN CONDITION THEN ACTION
CONDITION := predicate
           | CONDITION AND CONDITION
           | CONDITION OR CONDITION
           | ( CONDITION )
```

The exact simulation tick rate, action duration, and whether a rule may contain multiple immediate bookkeeping actions before one movement/combat action remain implementation decisions. A recommended constraint is one primary action per rule, with memory writes and signal sends represented as explicit actions.

## 10.3 Condition catalogue

| **Group**               | **Draft predicates**                                                                                |
|-------------------------|-----------------------------------------------------------------------------------------------------|
| Self and cargo          | carrying_component; carrying_nothing; health_below(percent); health_above(percent)                  |
| Location                | at_own_base; at_point(1..3); point_is_set(1..3); point_is_empty(1..3)                               |
| Forward vision          | sees_component; component_in_reach; sees_enemy_robot; sees_obstacle; visible_target_in_weapon_range |
| Radar                   | radar_detects_target; detected_target_in_weapon_range                                               |
| Communication           | received(COME_HERE); received(AVOID_HERE)                                                           |
| Movement / reachability | path_blocked; target_reached; target_unreachable                                                    |
| Combat                  | weapon_ready; has_weapon; enemy_visible                                                             |

Target-selection filters such as nearest, farthest, component type, enemy blueprint, or strongest/weakest enemy are desirable but not yet specified. The first version should at least support nearest valid target.

## 10.4 Action catalogue

| **Group**     | **Draft actions**                                                                                                                      |
|---------------|----------------------------------------------------------------------------------------------------------------------------------------|
| Movement      | move_forward; turn_left; turn_right; turn_random; stop                                                                                 |
| Navigation    | move_to_own_base; move_to_visible_target; move_to_radar_target; move_to_point(1..3); move_away_from_target; move_away_from_point(1..3) |
| Interaction   | pick_up_component; deposit_component_at_base; drop_component                                                                           |
| Combat        | attack_visible_target; attack_radar_target                                                                                             |
| Memory        | save_current_position(point); save_visible_target(point); save_radar_target(point); save_signal_position(point); clear_point(point)    |
| Communication | broadcast(COME_HERE); broadcast(AVOID_HERE)                                                                                            |

## 10.5 Evaluation semantics

- **Priority.** Rules are evaluated in visible top-to-bottom order.

- **First match wins.** Only the highest-priority matching rule controls the current cycle.

- **Continuous re-evaluation.** The robot rechecks the complete list repeatedly.

- **Pre-emption.** A newly matching higher rule may interrupt navigation, waiting, or combat.

- **No implicit reaction.** Seeing an enemy or receiving a signal has no effect unless a rule uses it.

- **Safe failure.** If no rule matches, the robot idles. Invalid or unreachable targets must not crash the simulation.

## 10.6 Memory semantics

- Each point stores exactly one coordinate or an empty value.

- Writing a point replaces its previous coordinate.

- Memory is private to the individual robot.

- Memory survives normal movement and program execution.

- All three points are cleared whenever the robot is reprogrammed at its base.

- Memory disappears when the robot is destroyed.

## 10.7 Example program: component scavenger

```text
1. WHEN at_own_base AND carrying_component
   THEN deposit_component_at_base

2. WHEN carrying_component
   THEN move_to_own_base

3. WHEN component_in_reach AND carrying_nothing
   THEN pick_up_component

4. WHEN radar_detects_target AND carrying_nothing
   THEN move_to_radar_target

5. WHEN sees_obstacle
   THEN turn_random

6. WHEN carrying_nothing
   THEN move_forward
```

Implementation note: the deposit rule must remain above the generic carrying rule. This example shows why rule ordering and action-completion semantics require clear editor feedback.

## 10.8 Example program: memory-assisted scout

```text
1. WHEN sees_component AND carrying_component
   THEN save_visible_target(Point 1)

2. WHEN at_own_base AND carrying_component
   THEN deposit_component_at_base

3. WHEN carrying_component
   THEN move_to_own_base

4. WHEN at_point(Point 1) AND component_in_reach AND carrying_nothing
   THEN pick_up_component

5. WHEN at_point(Point 1) AND carrying_nothing
   THEN clear_point(Point 1)

6. WHEN point_is_set(Point 1) AND carrying_nothing
   THEN move_to_point(Point 1)
```

This example also demonstrates the unresolved need for limited action sequences. Saving a coordinate and continuing movement may require either a zero-duration bookkeeping action or a rule that can contain a short sequence.

## 10.9 Example program: defensive responder

```text
1. WHEN health_below(25) AND sees_enemy_robot
   THEN move_away_from_target

2. WHEN received(COME_HERE)
   THEN save_signal_position(Point 2)

3. WHEN sees_enemy_robot AND visible_target_in_weapon_range
   THEN attack_visible_target

4. WHEN at_point(Point 2)
   THEN clear_point(Point 2)

5. WHEN point_is_set(Point 2)
   THEN move_to_point(Point 2)

6. WHEN radar_detects_target
   THEN move_to_radar_target

7. WHEN sees_obstacle
   THEN turn_random
```

## 10.10 Visual editor requirements

- Drag-and-drop ordered rule cards with obvious priority numbers.

- Nested AND/OR condition groups displayed without ambiguous precedence.

- Component-aware validation: do not silently allow radar actions on a blueprint without a radar or pickup actions without a manipulator.

- Warnings rather than hard errors for legal but ineffective behavior, such as a robot that can only move forward.

- Simulation-readable labels showing which rule currently controls a selected robot.

- Trace history for a selected robot: evaluated rules, chosen rule, target, memory changes, and received signals.

- Save, duplicate, rename, and version programs in the player library.

# 11. Suggested MVP Scope

The first playable version should validate whether designing autonomous colonies is understandable, strategic, and enjoyable to observe. It does not need final balance or every planned module.

## 11.1 MVP capabilities

- Server-authoritative real-time simulation that continues while players are offline.

- Lobby start flow with equal budgets, generated arena, human players, and AI opponents.

- Invulnerable bases, component inventory, approved blueprints, and automatic random production.

- Three locomotion types, three armor tiers, manipulator, three radars, and three weapons.

- Directional vision, pathfinding, terrain restrictions, three coordinate-memory cells, and two signals.

- Priority-rule program editor with AND/OR conditions.

- Recall, base-only reprogramming, and memory reset.

- Combat, partial component drops, collection, and match-end scoring.

- Complete observer map, robot inspection, colony statistics, and basic program tracing.

## 11.2 Recommended implementation separation

| **Subsystem**        | **Responsibility**                                                                                    |
|----------------------|-------------------------------------------------------------------------------------------------------|
| Simulation core      | Deterministic world state, ticks, movement, perception, combat, inventory, production, and scoring.   |
| Program runtime      | Rule validation, condition evaluation, target context, action execution, memory, and trace output.    |
| Server / match host  | Lobby, authoritative timing, persistence, reconnection, AI slots, and anti-cheat boundaries.          |
| Client visualization | Arena rendering, playback of authoritative state, robot/base inspection, statistics, and leaderboard. |
| Program editor       | Rule construction, component-aware validation, library management, and assignment to recalled robots. |

# 12. Open Design Decisions

The following items were not resolved in the concept discussion and should not be treated as fixed requirements.

| **Priority** | **Decision**            | **Question**                                                                                                                             |
|--------------|-------------------------|------------------------------------------------------------------------------------------------------------------------------------------|
| P0           | Rule action model       | Decide whether a rule supports one action, a short action sequence, or zero-duration memory/signal side effects plus one primary action. |
| P0           | Signal reach            | Confirm whether the shared friendly channel is global or limited by a communication radius.                                              |
| P0           | Carrying capacity       | Define manipulator slot limits and the number of components a robot may carry at once.                                                   |
| P0           | Starting allocation     | Define whether unspent starting budget becomes base inventory, is lost, or may be reserved for later production.                        |
| P0           | Target selection        | Define nearest/farthest/type filters and what information sensors reveal about components and enemy robots.                              |
| P0           | Scoring                 | Finalize survival status, component values, base inventory treatment, and tie-breaking.                                                  |
| P1           | Weapon modules          | Confirm whether two installed weapons may be identical and how a program selects which weapon fires.                                     |
| P1           | World visibility        | Decide whether the observer sees all loose components or only discovered ones.                                                           |
| P1           | Production timing       | Choose fixed or mass/component-dependent build time.                                                                                     |
| P1           | Terrain balance         | Finalize terrain classes, movement speeds, turning speeds, and absolute barriers.                                                        |
| P1           | Diplomacy and hostility | Define whether all other colonies are always enemies, whether alliances exist, and whether friendly fire is possible.                    |
| P1           | Match parameters        | Set supported duration, player count, map size, resource richness, and spawn-rate ranges.                                                |
| P2           | AI profiles             | Define computer-colony strategies for tutorial, peaceful competition, defense, and aggression.                                           |
| P2           | Spectating and replay   | Decide whether eliminated/inactive players can continue editing, spectate only, or inspect complete traces and replays.                  |

# 13. Acceptance Criteria for the First Prototype

- Two colonies can start from equal budgets on an unknown generated map.

- A scavenger can autonomously find, collect, and deposit a compatible component.

- The base can detect a buildable approved blueprint and produce it without a player command.

- A robot can navigate using forward vision, a specialist radar, its own base coordinate, and stored points.

- A robot can broadcast a signal and another robot can react only when its program contains a matching rule.

- An online player can recall one robot, reprogram it at the base, and observe that its memory was cleared.

- The same colony continues operating when its player disconnects and reconnects.

- Two armed robots can fight, a destroyed robot can drop a subset of components, and another robot can salvage them.

- The client can explain, for a selected robot, which priority rule is currently active and why.

- The match ends on time and produces a deterministic provisional score from the authoritative state.

> **Document status:** This specification captures the agreed game concept and a proposed language draft. Numeric balance, unresolved semantics, final scoring, and presentation style require additional design work before production implementation.
