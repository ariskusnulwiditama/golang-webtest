# DEVELOPMENT.md — WebTest Studio

Living progress log. Update this file at the end of every work session (rule 20).

## Current status

**Phases 1 and 2 (backend foundation + auth) complete and verified against the dev database.**
Phase 0 documents are unchanged; application code now exists — Go 1.25, module
`github.com/ariskusnulwiditama/golang-webtest`:

- `cmd/api` — HTTP server: config → logger → optional auto-migrate → database → router,
  graceful shutdown on SIGINT/SIGTERM, middleware chain (request id, access log, recovery,
  CORS, body limit) and JSON `/healthz` + `/readyz`.
- `cmd/migrate` — `up` / `down <n>` / `status` over the embedded SQL (golang-migrate + iofs).
- `migrations/000001_init.{up,down}.sql` — the complete schema from `docs/DATA-MODEL.md`
  (10 business tables + `execution_jobs` + `ws_tickets`), embedded by `migrations/embed.go`.
- `internal/platform/config` — godotenv loading, every setting validated and aggregated
  into one error, fail-fast defaults; refuses insecure production combinations.
- `internal/platform/logger` — slog text/JSON handler with service name and level.
- `internal/platform/apperr` — error taxonomy mapped to HTTP status + machine code.
- `internal/platform/httpx` — `{data, meta}` / `{error}` envelope, request id, access log,
  panic recovery, CORS, body limit.
- `internal/platform/database` — GORM handle + pgx pool (limits, ping at startup, redacted
  DSN in logs) and the migrator used by `cmd/migrate` / `DB_AUTO_MIGRATE`.
- `internal/platform/health` — liveness (never touches dependencies) and readiness (pings DB).
- `internal/features/auth` — user repository over `users` (citext email), bcrypt cost 12,
  register/login, `GET`/`PATCH /auth/me`, HS256 access tokens with the role claim and
  `RequireAuth` middleware. The middleware re-reads the account, so a deleted or deactivated user
  cannot keep using an unexpired token.
- `internal/platform/ratelimit` — per-IP token bucket in front of login (10/min) and register
  (5/min); in-process, which matches the single-replica assumption (AD-14, AD-15, AD-16 resolved).

Verified on 2026-11-22 (executed, not assumed):

- `go build ./...`, `go vet ./...`, `go test ./...` — clean; unit tests cover config
  (defaults, every validation rule, DSN escaping), the HTTP envelope/middleware and health.
- `go run ./cmd/migrate status` → `version=none`; `up` → version 1; second `up` → no-op;
  `down 1` leaves only `schema_migrations`; `up` restores all 13 tables.
- Live psql checks: action/one-target/browser CHECKs fire, partial unique index enforces one
  default environment per project, project names are unique per owner case-insensitively, and
  deleting a project cascades to suites → cases → steps, environments and executions.
- `bin/api.exe` against `wts-db`: `/healthz` 200 `{"data":{"status":"ok","version":"dev"}}`,
  `/readyz` 200 with `checks.database=ok`, unknown route → 404 envelope whose `request_id`
  matches the `X-Request-ID` header; startup log prints the DSN with `xxxxx` for the password.
- Phase 2 rehearsal on 2026-11-22 — 18/18 checks against `wts-db` with a real `bin/api.exe`:
  register 201 (`role=user`, email normalised to lower case, no `password` string anywhere in the
  body, 308-character bearer token with `expires_at` +24 h); `GET /auth/me` 200 with the same id;
  `GET /auth/me` without a token → 401 `unauthorized` whose `request_id` matches `X-Request-ID`;
  `PATCH /auth/me` name 200; `PATCH` password 200 then the old password → 401 and the new one → 200;
  wrong password and unknown email both 401 with the identical message; duplicate register 409
  `conflict`; invalid payload 422 with `details` for all three fields; 14 rapid logins → 8×429
  `rate_limited` with `Retry-After`; a garbage token → 401; `/healthz` still 200.
- `go test ./...` = 7 packages ok: `auth` (service/repository rules, token attacks, handler flows),
  `ratelimit` (burst, refill, Retry-After, sweep, 429 envelope), plus the Phase 1 packages.

## Verified environment (checked, not assumed)

| Tool | Status |
|---|---|
| Go | 1.25.0 windows/amd64 — available |
| Git | 2.56.0 — available |
| Node.js / npm | 22.23.5 (volta) / 10.9.9 — available |
| Python / pip | 3.12.10 / 25.2 (scoop) — used to install the compose provider |
| Podman | 6.2.0 (scoop); WSL machine `podman-machine-default` (AlmaLinux-10, amd64, 8 CPU, **2 GiB RAM**) |
| `podman compose` | **working** — `podman-compose` 1.6.0 provider (`pip install podman-compose`) |
| Dev database | **running** — container `wts-db` (`postgres:16-alpine`, PostgreSQL **16.16**), healthy on `127.0.0.1:5432` |
| Docker Desktop | not installed; Podman is the container runtime |
| Workspace `d:\code\goproject2` | Go module scaffolded (`cmd/`, `internal/`, `migrations/`, `.editorconfig`, `.gitattributes`); `web/`, `worker/` pending; git repository on `main`, Phase 1 committed (`8f75cbf`, 30 files) |

Implication: the whole stack (Go + Node + containers) can be built and verified locally.
Verified end-to-end on 2026-11-22, not assumed: `podman compose up -d db` → container reports
`healthy` (healthcheck `pg_isready`) → `gen_random_uuid()` works → `citext` and `pgcrypto` are
available in `pg_available_extensions` → a throwaway Go program using `github.com/jackc/pgx/v5`
connected from the Windows host over TCP and printed `OK db=webtest`. Risk R1 is now closed.

Remaining operational notes:
- The WSL-backed Podman VM has **2 GiB RAM** and WSL machines **cannot** be resized with
  `podman machine set --memory` (it errors: "changing memory not supported for WSL machines").
  Before Phase 4, add `memory=6GB` to the `[wsl2]` section of `%UserProfile%\.wslconfig` and run
  `wsl --shutdown`; Chromium + Firefox + WebKit do not fit in 2 GiB.
- The Podman machine stops when WSL idles out; `podman machine start` reconnects it (the compose
  container then comes back on its own thanks to `restart: unless-stopped`).

## Architecture decisions (ADR-lite)

| ID | Decision | Rationale |
|---|---|---|
| AD-1 | Modular monolith, feature-first Go packages with handler/service/repository layers | Spec requirement; far cheaper than microservices at this scale |
| AD-2 | Job queue in PostgreSQL via `FOR UPDATE SKIP LOCKED` instead of Redis/RabbitMQ/gRPC | Zero extra infra, transactional with `test_executions`, crash-safe, cancellation via heartbeat |
| AD-3 | Worker pulls jobs; API never pushes | Backpressure + restart safety + atomic claiming with N workers |
| AD-4 | Worker uploads screenshots; API owns storage + authorization | Worker stays stateless and DB-free |
| AD-5 | Migrations via golang-migrate, GORM never AutoMigrates | Spec: schema changes only through migrations |
| AD-6 | Step results stored as `execution_results.steps jsonb` | Immutable snapshot, no 11th table, no row explosion |
| AD-7 | `test_cases` has no denormalised `project_id` | Joins through `test_suites` prevent drift |
| AD-8 | WebSocket auth via short-lived single-use ticket | Avoids JWTs in URLs/logs |
| AD-9 | Recorder = interactive CDP screencast + input relay (Tier 1), assisted picker fallback (Tier 2) | Only realistic way to record inside a browser UI; fallback pre-approved |
| AD-10 | Manual constructor DI in `cmd/api/main.go` | Simple, explicit, testable; no fx/wire |
| AD-11 | **PostgreSQL 16** stays (SQLite rejected) | SQLite has a single writer (no `SKIP LOCKED`), no `jsonb`/`citext`/`text[]`, no deferrable unique constraint, and every future `ALTER TABLE` becomes a table rebuild. Postgres is now as cheap to run as SQLite (verified container) and all approved docs stay valid |
| AD-12 | Podman + `podman-compose` is the container runtime | Already installed and verified; cross-platform and rootless. `compose.yaml` avoids provider-specific features so `docker compose` also works |
| AD-13 | Gin + GORM (pgx) + golang-migrate with `embed.FS` | Gin supplies routing/binding/validation; GORM keeps repositories terse over pgx; embedding migrations makes `cmd/api` and `cmd/migrate` self-contained binaries that need no source tree at runtime |
| AD-14 | **Access-token only** — HS256 JWT, 24 h TTL, no refresh tokens, client-side logout | Least moving parts for v1: no refresh table, no rotation/reuse-detection logic, no cookie or CSRF surface — exactly the documented v1 limitation. The upgrade is purely additive (`000002_refresh_tokens` + `POST /auth/refresh` + rotation on reuse) and the `Tokenizer` seam already isolates signing from handlers |
| AD-15 | **Projects are owner-only** — no `project_members` table in v1 | A single `owner_id` predicate in one authorizer is easier to audit and test than a membership join, and keeps Phase 3 at its planned size. Sharing later is additive: nullable member table plus one policy function behind the existing authorizer |
| AD-16 | **Recorder ships Tier 2 (assisted picker) first**; Tier 1 screencast becomes a time-boxed spike after core features (refines AD-9's ordering, not its intent) | The 2 GiB Podman VM cannot host multi-browser execution plus CDP screencast comfortably, and R2 is the highest-risk item. Tier 2 still produces real selectors from a real browser, so Phase 5 delivers value without the riskiest subsystem |

## Module tracker

| Module | Status |
|---|---|
| Planning / architecture | Done (this document set) |
| Repo scaffold + tooling | Done — Go module `github.com/ariskusnulwiditama/golang-webtest`, `.editorconfig`, `cmd/`, `internal/`, `migrations/`; `web/` and `worker/` land in their phases |
| Dev database (compose + verification) | Done — `wts-db` healthy, host↔DB TCP path proven with pgx v5 |
| Backend foundation (config, logger, db, migrations, health, errors, DI, shutdown) | Done — `cmd/api`, `cmd/migrate`, `000001_init` applied and rolled back, unit tests green |
| Auth (register, login, JWT, middleware, profile) | Done — `internal/features/auth` + `internal/platform/ratelimit`; 18/18 live checks; access-token only (AD-14) |
| Projects + environments CRUD | Not started |
| Suites CRUD | Not started |
| Test cases + steps (duplicate, reorder) | Not started |
| Worker: runner + 14 actions | Not started |
| Execution orchestration + queue + WS | Not started |
| Reports + dashboard endpoints | Not started |
| Recorder | Not started |
| Frontend foundation + layout | Not started |
| Frontend feature pages | Not started |
| Integration / E2E / hardening | Not started |
| Docker Compose + docs | Not started |

## Known issues / constraints

- Podman VM memory is 2 GiB and WSL machines **cannot** be resized with `podman machine set`;
  raise it before Phase 4 via the `[wsl2]` section of `%UserProfile%\.wslconfig`
  (`memory=6GB`, then `wsl --shutdown`).
- The Podman machine shuts down when WSL idles out; run `podman machine start` before using the DB.
- `podman-compose` 1.6.0 is a community provider — it handles services, `container_name`, volumes,
  healthchecks, `depends_on` and named volumes, but it is not the whole Compose spec. Keep
  `compose.yaml` conservative (no `profiles`, no `deploy.resources`, no `extends`).
- JWT logout is client-side only (no revocation list) in v1.
- Rate limiting is in-process (single-replica assumption) in v1.
- `execution_logs` and `test_screenshots` will need a retention job before production use.

## Running the dev database

```powershell
podman machine start              # the WSL machine stops when idle
podman compose up -d db           # starts wts-db (postgres:16-alpine)
podman ps --filter name=wts-db    # expect "Up ... (healthy)"
podman compose logs -f db         # follow server logs
podman compose down               # stop, keep data
podman compose down -v            # stop and DELETE the wts-db-data volume
```

Connection string (values come from `.env`):
`postgres://webtest:<password>@127.0.0.1:5432/webtest?sslmode=disable`.
The port is published on loopback only. No host `psql` is installed — use the one in the container:

```powershell
podman exec -it wts-db psql -U webtest -d webtest
```

## Running the API and migrations

```powershell
podman machine start; podman compose up -d db   # database first (loopback only)
go run ./cmd/migrate up                         # apply pending migrations (idempotent)
go run ./cmd/migrate status                     # version=1 dirty=false applied=1
go run ./cmd/migrate down 1                     # roll the last migration back
go run ./cmd/api                                # serves on 127.0.0.1:8080

# in another shell
Invoke-RestMethod http://127.0.0.1:8080/healthz   # liveness + version, no dependencies
Invoke-RestMethod http://127.0.0.1:8080/readyz    # pings the database, 503 when it is down

# auth round trip — register logs in, then reuse the token (contract: docs/ARCHITECTURE.md §8)
$session = Invoke-RestMethod -Method Post http://127.0.0.1:8080/api/v1/auth/register `
  -ContentType 'application/json' `
  -Body '{"email":"qa@example.com","password":"long-enough-pass","name":"QA Bot"}'
Invoke-RestMethod http://127.0.0.1:8080/api/v1/auth/me `
  -Headers @{ Authorization = "Bearer $($session.data.token)" }
```

`DB_AUTO_MIGRATE=true` (the `.env.example` default) applies pending migrations during API startup;
config validation rejects that combination when `APP_ENV=production`, where `cmd/migrate` runs as
its own deployment step. Versioned build:
`go build -trimpath -ldflags "-X main.version=1.0.0" -o bin/api ./cmd/api`.

Error contract, fixed for every endpoint from here on:

```json
{"error":{"code":"not_found","message":"route not found","details":{},"request_id":"..."}}
```

Success responses are `{"data": ...}` with `meta` added for list endpoints. `X-Request-ID` is echoed
on every response and appears in every log line, so a user-reported id maps to a server log entry;
the same id is generated when the client sends an invalid one.

## Decisions taken 2026-11-22

All three open questions were answered; the rationale lives in the ADR table (AD-14 … AD-16):

1. **Auth scope — access-token only.** HS256, 24 h, client-side logout, no refresh table.
   Refresh tokens + revocation are an additive later step (`000002_refresh_tokens`, `POST
   /auth/refresh`) if session length becomes a real complaint.
2. **Project access — owner-only.** `project_members` is deferred; Phase 3 keeps every query
   behind one authorizer so the swap is a single function.
3. **Recorder — Tier 2 first.** The assisted picker is the v1 deliverable; the Tier 1 screencast
   becomes a time-boxed spike once the core features are done.

Also resolved earlier: module path `github.com/ariskusnulwiditama/golang-webtest`; database engine
PostgreSQL 16 (AD-11); `podman compose` provider; toolchain versions.

## Next tasks

1. Phase 3 — projects + environments CRUD with owner scoping (AD-15), pagination caps and the
   shared envelope; then suites, test cases and step reorder (`DEFERRABLE` unique already in place).
2. Consider wiring Swagger UI early (`_test`-guarded `swag init` import) once the first CRUD module
   exists — cheap demo value before the frontend lands.
3. Create the GitHub remote and push `main` when a repository name/visibility is chosen.
4. Keep the module tracker current and ship tests with every module, recording the actual
   `go test ./...` output here.

