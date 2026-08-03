# robocolony

Autonomous robot colony simulation. You are an engineer of behavior, not a unit
commander: you design robot blueprints and priority-rule programs, then watch
your colony compete on a generated arena — including while you are offline.

Server-authoritative real-time simulation in Go, vanilla HTML/JS client served
by the same binary.

- Game design: [`docs/design_v0.1.md`](docs/design_v0.1.md)
- Architecture & conventions for contributors and agents: `AGENTS.md`

## Status

Pre-alpha. Building the POC: Google-OIDC login → lobby → generated arena →
robots executing rule programs, observable live in the browser.

## Stack

| Layer | Choice |
|---|---|
| Server / simulation | Go, stdlib `net/http` |
| Live state to client | Server-Sent Events (stdlib, no WebSocket dep) |
| Client | Vanilla HTML/JS + `<canvas>`, no build step |
| Persistence | SQLite (`modernc.org/sqlite`, CGO-free) — users, programs, blueprints |
| Auth | Google OIDC |
| Deploy | GitHub Actions → GHCR (SHA-tagged) → `deploy` branch → Portainer webhook, behind Traefik |

## Local run

```sh
cp .env.example .env   # fill in Google OAuth client id/secret
go run ./cmd/server    # http://localhost:8080
```

## License

MIT — see [LICENSE](LICENSE).
