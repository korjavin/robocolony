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

`GET /health` returns `{"status":"ok"}` with no auth and no database
dependency — it is also the container healthcheck. `SIGINT`/`SIGTERM` trigger a
logged graceful shutdown with a 30s drain.

## Google sign-in setup

The server refuses to start without an OAuth client, because every route but
`/health` and the static shell needs a session.

1. Google Cloud console → **APIs & Services → Credentials → Create credentials
   → OAuth client ID → Web application**.
2. **Authorised redirect URI**: `<BASE_URL>/auth/callback` — exactly, including
   the scheme and with no trailing slash. Add one per environment, e.g.
   `http://localhost:8080/auth/callback` and
   `https://robocolony.example.com/auth/callback`. It must match byte for byte
   or Google returns `redirect_uri_mismatch`.
3. On the **OAuth consent screen**, the `openid`, `email` and `profile` scopes
   are all that is requested. While the app is in testing, add each player as a
   test user.
4. Put the client id and secret in `GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET`.

Sessions are a random 32-byte cookie token; only its SHA-256 is stored, the
expiry is 30 days and slides on use, and the cookie is `HttpOnly`,
`SameSite=Lax` and `Secure` whenever `BASE_URL` is `https://`.

Checks that must pass before a PR:

```sh
go vet ./... && go build ./... && go test ./... && gofmt -l .
```

## Configuration

Every variable is documented in [`.env.example`](.env.example). `.env` is
gitignored; production values are Portainer stack environment variables.

| Variable | Default | Read by |
|---|---|---|
| `PORT` | `8080` | server |
| `LOG_LEVEL` | `info` | server (`debug`/`info`/`warn`/`error`) |
| `BASE_URL` | `http://localhost:8080` | server |
| `DB_PATH` | `./data/robocolony.db` | server |
| `GOOGLE_CLIENT_ID` | — | server — **required**, startup fails without it |
| `GOOGLE_CLIENT_SECRET` | — | server — **required**, startup fails without it |
| `GOOGLE_REDIRECT_URL` | `BASE_URL` + `/auth/callback` | server (override only if the console entry differs) |
| `HOSTNAME` | — | compose/Traefik only |
| `TRAEFIK_NETWORK_NAME` | `traefik_default` | compose/Traefik only |
| `TRAEFIK_CERT_RESOLVER` | `myresolver` | compose/Traefik only |
| `TRAEFIK_CERT_DOMAIN` | `HOSTNAME` | compose/Traefik only |

## Deployment

Push to `master` → GitHub Actions builds the image and pushes it to
`ghcr.io/korjavin/robocolony:<commit-sha>` (never `:latest`) → the workflow
force-pushes a `deploy` branch whose `docker-compose.yml` pins that SHA →
a Portainer webhook redeploys the stack.

**Required GitHub secret** (Settings → Secrets and variables → Actions):

| Secret | Value |
|---|---|
| `PORTAINER_REDEPLOY_HOOK` | Stack webhook URL from Portainer (stack → Webhooks) |

**Portainer stack setup:**

1. Create the stack from this repository and point it at the **`deploy`
   branch**, never `master`. `master` carries a `:latest` placeholder tag.
2. Set the stack environment variables: `HOSTNAME`, `TRAEFIK_NETWORK_NAME`,
   `TRAEFIK_CERT_RESOLVER`, `TRAEFIK_CERT_DOMAIN`,
   `BASE_URL`, `LOG_LEVEL`, `DB_PATH`, `GOOGLE_CLIENT_ID`,
   `GOOGLE_CLIENT_SECRET`. `BASE_URL` must be the public `https://` origin —
   the session cookie's `Secure` flag is derived from its scheme.
3. No host setup is needed for storage. The container runs as **uid/gid 1000**
   and the SQLite file lives in the `robocolony_data` named volume, which
   Docker seeds from the image's `/app/data` and therefore creates already
   owned by that user. A bind mount would arrive root-owned and break writes.

## License

MIT — see [LICENSE](LICENSE).
