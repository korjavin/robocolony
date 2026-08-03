# AGENTS.md — conventions for contributors and coding agents

Read this before writing code. `docs/design_v0.1.md` is the game design; this
file is the *implementation* contract. Where the design doc leaves a decision
open (§12), the "Locked decisions" table below is the answer for the POC.

## Layout

```
cmd/server/          entrypoint: config, db, routes, graceful shutdown
internal/sim/        pure simulation: world, tick, movement, vision, combat.
                     NO net/http, NO database/sql, NO globals. Deterministic.
internal/prog/       program runtime: rule model, conditions, actions, memory.
                     Depends on internal/sim types only.
internal/auth/       Google OIDC, sessions
internal/lobby/      lobby lifecycle, match registry
internal/db/         SQLite open + embedded goose migrations + queries
internal/server/     HTTP handlers, SSE stream, JSON DTOs
web/                 index.html, css/, js/ — vanilla, served by Go, no build step
sql/migrations/      NNN_name.sql, goose format
```

## Locked decisions (POC)

| Topic | Decision |
|---|---|
| World model | **Grid.** Integer cell coordinates, 8 headings. No continuous physics. Pathfinding = BFS over passable cells. |
| Determinism | `internal/sim` must be deterministic given (seed, inputs). Use an explicit `*rand.Rand` seeded per match — **never** the global `math/rand` functions, never map-iteration order for anything that affects state. |
| Tick rate | 10 ticks/sec, fixed. Client snapshots at 10/s. |
| Client transport | **SSE** (`text/event-stream`) for world snapshots; plain `POST` JSON for player commands. Do NOT add a WebSocket dependency. |
| Rule action model (design §12 P0) | Memory writes and `broadcast(...)` are **zero-tick side effects**: they execute and evaluation *continues* down the rule list. The first matching rule with a *primary* action (movement / navigation / interaction / combat) ends the tick. |
| Signal reach (design §12 P0) | Global friendly channel. No radius. |
| Persistence | SQLite via `modernc.org/sqlite` (CGO-free — builds must stay `CGO_ENABLED=0`). Users, sessions, blueprints, programs, lobbies are persisted. The live world stays in memory, but a running match **survives a restart**: each one persists its seed and its ordered player-command log (`internal/lobby/persist.go`) and is replayed at startup. Every mutation of a live world that is not a tick must go through `Match.Apply`, or the replay rebuilds a world in which it never happened. |
| Auth | Google OIDC. Every non-static route except `/health` and the auth callbacks requires a session. |
| Go version | `go 1.25.0` in `go.mod`, `golang:1.25-alpine` builder. CI is the build authority; locally `GOTOOLCHAIN=auto` fetches 1.25.0 on first build. |
| Logging | `log/slog`, structured. No `fmt.Println`, no third-party logger. |
| Deps | Stdlib first. A new module dependency needs a one-line justification in the PR body. |

## Landmines

- **`internal/sim` purity.** If you need HTTP, DB, or wall-clock time inside
  `internal/sim`, you are in the wrong package. Time is *ticks*, passed in.
- **Determinism guard.** `internal/sim` has a test that runs a seeded world
  twice and compares a state hash. Anything nondeterministic (global rand, map
  ordering, goroutine races) fails it. Do not weaken the test to make it pass.
- **Never edit an existing migration.** Add `NNN_next.sql`. Numbers must be
  contiguous.
- **`CGO_ENABLED=0`** everywhere. Do not introduce `mattn/go-sqlite3`.
- **Never `:latest` in a deployed compose.** CI rewrites the image tag to the
  commit SHA on the `deploy` branch; `master` carries `:latest` as a
  placeholder only.
- **Secrets come from env**, never a committed file. `.env` is gitignored;
  `.env.example` documents every variable.
- **`//go:embed` paths are relative to the `.go` file** — a common breakage
  when moving files.
- **The client has no build step.** Do not add npm, bundlers, or a framework to
  `web/`. Plain `<script>` tags and ES modules.

## Verification

Every PR must pass:

```sh
go vet ./...
go build ./...
go test ./...
gofmt -l .        # must print nothing
```

Non-trivial logic ships with a test in the same package. Table-driven where it
fits. No test frameworks beyond `testing`.

## Workflow

- Issues are tracked in **beads** (`bd list`, `bd show <id>`). Not TODO
  comments, not markdown checklists.
- Branch off `origin/master`, one bead (or one coherent group) per PR, draft PR
  for review. `git fetch origin master` first — worktrees can be cut stale.
- Merge commits only. Never squash, never rebase-merge, never push to `master`.
- Flag deferrals honestly in the PR body rather than silently dropping scope.
