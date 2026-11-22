# WebTest Studio — Architecture & Implementation Plan

**Status:** Draft — awaiting approval. No application code has been written yet.
**Date:** 2026-11-22
**Scope:** answers Prompt 1 tasks 1–10. Data model -> `docs/DATA-MODEL.md`,
job protocol -> `docs/WORKER-PROTOCOL.md`, progress log -> `DEVELOPMENT.md`.

## 1. Product summary

WebTest Studio is a web-based, no-code browser test automation platform (Katalon-style):

- create **projects** (ownership container),
- organise **test suites** → **test cases** → **test steps** (typed JSON actions),
- **record** real browser interactions and turn them into steps,
- **execute** steps on Chromium / Firefox / WebKit through a Node.js Playwright worker,
- watch executions **live** (WebSocket), inspect logs + screenshots,
- read **reports** and **dashboard** analytics.

**Non-goals for v1** (explicitly out): team/permission management beyond owner scoping,
scheduled runs + CI webhooks (API-triggered runs only), load testing, visual-regression
diffing, plugin marketplace, fan-out parallel execution of a single suite, audit UI.

## 2. System context

```
 Browser ──► SPA (Vue 3 + TS + Vite + Pinia + Tailwind)
              │ REST /api/v1            │ WS /api/v1/ws/*
              ▼                         ▼
        API — Go 1.25 + Gin (modular monolith) ◄────► PostgreSQL 16
              │  auth │ projects │ suites │ cases        (data + job queue)
              │  executions │ reports │ recorder
              │  worker gateway adapter
              │ internal HTTP job protocol        local FS (or S3) screenshot store
              ▼                                          ▲
        Worker — Node 22 + Playwright (separate process) ┘ (uploaded PNGs)
              ▼
        Chromium / Firefox / WebKit
```

Four processes (`web`, `api`, `worker`, `postgres`), one database, no message broker,
no microservices. The job queue is a PostgreSQL table consumed with
`FOR UPDATE SKIP LOCKED` — transactional, restart-safe, zero extra infrastructure.

## 3. Component responsibilities

| Component | Owns | Must not |
|---|---|---|
| **SPA** | UX, client state, optimistic reorder, live-view rendering, form validation | Talk to the worker, own business rules |
| **API (Go)** | Authn/z, validation, business rules, ordering/transactions, persistence, job lifecycle, WS fan-out, screenshot authorization | Launch browsers, embed Playwright internals, auto-migrate in prod |
| **Worker (Node)** | Browser lifecycle, step execution, event capture, screenshots, timeouts, cancellation, cleanup | Touch the database, know about users/projects, decide authorization |
| **PostgreSQL** | Source of truth, referential integrity, job claiming/locking | Hold browser session state |

Clean boundary: the API knows *jobs*, the worker knows *steps*. Only two JSON
contracts cross the wire (`docs/WORKER-PROTOCOL.md`).

## 4. Backend: modular monolith layout

```
cmd/
  api/main.go            # composition root: config -> logger -> db -> repos -> services -> router
  migrate/main.go        # up / down / status CLI (golang-migrate + embedded FS)
internal/
  platform/              # cross-cutting, no business rules
    config/              # env parsing + validation (fail fast)
    logger/              # log/slog JSON handler, request-scoped logger
    database/            # GORM open (pgx), pool tuning, transaction manager
    migrations/          # embed.FS of ../../migrations
    httpx/               # response envelope, error mapper, pagination helpers
    apperr/              # typed errors {Code, HTTPStatus, Message, Details}
    validate/            # go-playground/validator custom rules (action types, uuid...)
    authctx/             # principal in request context
    clock/               # Clock interface (testability)
  auth/                  # model, dto, repository, service, handler, jwt, password
  project/               # + environments sub-resource
  suite/
  testcase/              # includes steps + duplicate/reorder
  execution/             # orchestration, dispatcher, reaper, results
  workerclient/          # adapter: HTTP client for the Node worker (interface + impl)
  realtime/              # WS hub: execution rooms + recorder rooms, ws tickets
  recorder/              # session orchestration + relay to worker
  report/                # read-only aggregation queries
  dashboard/
  storage/               # Storage interface: Save/Open/Delete; local FS impl
migrations/              # 000001_init.up.sql / .down.sql
docs/                    # architecture, data model, protocol, openapi
```

### Layering rules (SOLID without ceremony)

- `handler` — bind/validate DTO, map DTO↔domain, call service, map errors. **No business branching.**
- `service` — business rules, authorization checks, transactions, orchestration.
- `repository` — GORM/SQL only; accepts an optional `tx` for composition.
- Interfaces are declared **where consumed** (Go idiom) and only where substitution is
  actually needed: `workerclient.Client`, `storage.Storage`, `clock.Clock`,
  `realtime.Publisher`, and repositories that unit tests must fake.
- DI is **manual constructor injection** in `cmd/api/main.go`. No fx/wire/dig.
- Context-first signatures, `fmt.Errorf("...: %w", err)`, no `panic` in the request path,
  no package-level mutable state.

### Cross-cutting decisions

- **Errors:** one `apperr.Error` type → one Gin middleware maps to HTTP:
  `{"error":{"code":"VALIDATION_ERROR","message":"...","details":[...],"request_id":"..."}}`.
- **Responses:** `{"data": ...}` or `{"data": [...], "meta": {page,page_size,total,total_pages}}`.
- **Validation:** Gin binding + validator v10 with custom rules (action enum, uuid, url).
  Unknown fields are rejected on write DTOs (`DisallowUnknownFields`).
- **Migrations:** golang-migrate + `embed.FS`, executed by `cmd/migrate` and by the API
  container entrypoint. `AutoMigrate` is not used anywhere.
- **Logging:** `log/slog` JSON; request-id middleware (`X-Request-ID`) propagated to worker calls.
- **Shutdown:** `signal.NotifyContext` → HTTP `Shutdown` (timeout) → stop dispatcher/reaper → close DB.

## 5. Job execution architecture (Go ⇄ Node)

**Pull-based queue in PostgreSQL** (full contract in `docs/WORKER-PROTOCOL.md`):

1. `POST /api/v1/executions` inserts `test_executions(status=queued)` + `execution_jobs(status=queued, payload=snapshot)`.
2. Worker `POST /internal/v1/jobs/claim {worker_id, capacity}` (long-poll ≤ 20 s).
   Go claims with `UPDATE ... FOR UPDATE SKIP LOCKED` → `claimed`, returns payload + `job_id`.
3. Worker heartbeats every 5 s (`POST /jobs/:id/heartbeat`), Go mirrors state to
   `running` + `last_heartbeat_at` and returns `{cancel: bool}` in the heartbeat response.
4. Worker streams batched logs (`POST /jobs/:id/logs`, idempotent on `seq`) and uploads screenshots.
5. Worker finishes with `POST /jobs/:id/complete {status, results, logs?}` (idempotent by `job_id`).
6. A **reaper** goroutine marks jobs `timeout` when `last_heartbeat_at` is stale (> 60 s) or the
   job exceeds its max duration, then finalizes the execution and publishes a WS event.

Why pull over push/gRPC/RabbitMQ:

| Concern | Pull + Postgres |
|---|---|
| Worker crash mid-job | job stays `claimed`; reaper/stale-heartbeat recovery; no lost work |
| Backpressure | worker declares capacity, claims only what it can run |
| Extra infra | none (Postgres is already required) |
| Cancellation | heartbeat round-trip carries the cancel flag |
| At-least-once | completion is idempotent, protected by `(job_id)` uniqueness |

Rejected: gRPC bidi streaming (protoc + codegen complexity, no benefit at this scale),
RabbitMQ/Redis (extra infrastructure, weaker transactional coupling with `test_executions`),
direct HTTP push (unbounded retries, ambiguous delivery, no capacity control).

**Multiple workers** are supported later with zero protocol change (claim is atomic).

## 6. Realtime (WebSocket)

- Handshake: `POST /api/v1/auth/ws-ticket` → 60 s single-use ticket (opaque UUID in
  `ws_tickets`, hashed, in DB not memory so multiple API replicas stay correct). The SPA
  connects to `/api/v1/ws/executions/:id?ticket=...`. A raw JWT never appears in a URL.
- Rooms keyed by `execution:{id}` and `recorder:{sessionId}`; the hub is a plain
  goroutine-per-connection with `chan []byte` and a slow-consumer drop policy.
- Events: `execution.queued|started|step|log|finished|cancelled|heartbeat`, `recorder.frame`,
  `recorder.step`, `recorder.status`. Every event carries `seq` so the client can detect gaps.
- Graceful degradation: if WS fails the UI polls `GET /api/v1/executions/:id` every 2 s.

## 7. Frontend structure

```
web/src/
  api/          axios instance, endpoint modules, generated-free typed DTOs (types/api.ts)
  components/   ui/ (Button, Input, Select, Modal, ConfirmDialog, Toast, Badge, Card,
                Table, Tabs, Spinner, EmptyState, ErrorState, Skeleton, Pagination),
                charts/ (hand-rolled SVG Line/Bar), layout/ (AppShell, Sidebar, Header)
  composables/  useForm (zod-backed), useAsync, usePolling, useToast, useWebSocket
  layouts/      AuthLayout, DashboardLayout
  pages/        Login, Register, Dashboard, Projects, ProjectDetail, SuiteDetail,
                TestCaseEditor, Recorder, Executions, ExecutionDetail, Reports, Settings, NotFound
  router/       routes + auth/guest guards + lazy chunks
  stores/       auth, ui(theme/toasts), projects, suites, testCases, executions, recorder
  types/        api.ts, recorder.ts, ws.ts
  styles/       main.css (Tailwind layers + design tokens)
```

Design language: dark-first, Linear/GitHub-Actions inspired, 8-pt spacing, one accent
colour, monospace for selectors/IDs, `lucide-vue-next` icons, custom SVG charts (no chart
library). Theme toggled via `class="dark"` + CSS variables, persisted in `localStorage`.
Every list page has loading (skeleton), empty, and error states; destructive actions use
`ConfirmDialog`; results reported through toasts; forms validated with zod before submit.

## 8. REST API surface (`/api/v1`, JSON, JWT bearer)

| Method + path | Purpose |
|---|---|
| `POST /auth/register`, `POST /auth/login` | create account / obtain access token |
| `GET /auth/me`, `PATCH /auth/me` | profile read / update (name, password) |
| `POST /auth/ws-ticket` | single-use 60 s WS ticket |
| `GET|POST /projects` | list (paginated, `q`, sort) / create |
| `GET|PATCH|DELETE /projects/:projectId` | detail (counts) / update / delete (cascade) |
| `GET|POST /projects/:projectId/suites` | list / create |
| `PATCH|DELETE /suites/:suiteId` | update / delete |
| `PATCH /projects/:projectId/suites/reorder` | `{suite_ids: []}` |
| `GET|POST /projects/:projectId/test-cases` | list across suites (filter `suite_id`, `priority`, `tags`, `q`) / create |
| `GET /suites/:suiteId/test-cases` | list ordered by `position` |
| `GET|PATCH|DELETE /test-cases/:testCaseId` | detail (with steps) / update / delete |
| `POST /test-cases/:testCaseId/duplicate` | clone case + steps (`name` suffix "(copy)") |
| `PATCH /test-cases/:testCaseId/reorder` | `{position}` or `{suite_id, position}` (move) |
| `GET|POST /test-cases/:testCaseId/steps` | list / append step |
| `PATCH|DELETE /steps/:stepId` | update / delete |
| `PUT /test-cases/:testCaseId/steps/reorder` | `{step_ids: []}` (full permutation, one tx) |
| `POST /steps/:stepId/duplicate` | clone single step |
| `GET|POST /projects/:projectId/environments` | list / create |
| `PATCH|DELETE /environments/:envId` | update / delete |
| `POST /executions` | enqueue: `{project_id, suite_id \| test_case_id, browser, environment_id?, headless?, options?}` |
| `GET /executions` | history: `project_id`, `status`, `browser`, `from`, `to`, `q`, pagination, sort |
| `GET /executions/:id` | detail + results + step snapshot |
| `POST /executions/:id/cancel` | request cancellation (queued → immediate, running → flag) |
| `GET /executions/:id/logs?after_seq=` | incremental logs |
| `GET /executions/:id/screenshots` | screenshot metadata list |
| `GET /screenshots/:id` | PNG bytes (auth + ownership checked) |
| `GET /reports/summary`, `/reports/trend`, `/reports/failures` | aggregates (`project_id`, `from`, `to`, `granularity`) |
| `GET /dashboard/stats` | cards + trend + recent + failures in one round trip |
| `POST /recorder/sessions` | `{project_id, url, browser}` → starting a session |
| `GET /recorder/sessions/:id` | status + captured steps |
| `POST /recorder/sessions/:id/stop` | stop and return final steps |
| `POST /recorder/sessions/:id/save` | `{suite_id, name, steps[]}` → creates test case |
| `DELETE /recorder/sessions/:id` | abort and clean up |
| `GET /healthz`, `GET /readyz` | liveness / readiness (db + worker reachability) |
| `GET /swagger/index.html` | Swagger UI (`swaggo/swag`, annotations in handlers) |

Internal worker API (`/internal/v1`, `X-Worker-Token` shared secret, never exposed publicly):

```
POST /jobs/claim              long-poll up to 20s, capacity-based
POST /jobs/:id/heartbeat      -> { cancel: bool }
POST /jobs/:id/logs           batched, idempotent on seq
POST /jobs/:id/screenshots    multipart upload
POST /jobs/:id/complete       final result (idempotent)
POST /jobs/:id/fail           crash/abort report
POST /recorder/:id/frame      screencast frame relay (binary)
GET  /recorder/:id/commands   pending input events for the browser (long-poll)
POST /recorder/:id/event      captured interaction batches + session end
```

Conventions: `2xx` envelope `{data, meta?}`; `4xx/5xx` envelope `{error{code,message,details,request_id}}`;
`400` validation, `401` unauthenticated, `403` forbidden, `404` not found, `409` conflict
(duplicate name, reorder mismatch), `422` domain rule violation, `429` rate limited,
`500` internal, `503` dependency unavailable.

## 9. Recorder architecture (kept out of the execution engine)

The recorder shares only the *browser factory* and *selector heuristics* with the executor;
sessions, event capture, and the relay live in `worker/src/recorder` + `internal/recorder`.

**Tier 1 (target): interactive streamed recording.** Worker launches Chromium (headful under
Xvfb inside the container runtime — Podman locally, Docker-compatible image), attaches a recorder
instrumentation script (`page.addInitScript`) that listens
in the capture phase without `preventDefault`, so normal site behaviour is untouched. Frames go
out via CDP `Page.startScreencast` (JPEG, ~10 fps, quality 60) → Go WS relay → `<canvas>` in the
SPA. User input from the SPA is mapped back to viewport coordinates and dispatched with
`Input.dispatchMouseEvent` / `insertText` / `dispatchKeyEvent`.

**Tier 2 (fallback, decided at Phase 5 spike): assisted picker.** If streamed interactivity proves
unreliable (latency, IME, coordinate scaling), the recorder degrades to: worker opens the URL,
user picks from a rendered element tree / clicks a screenshot region, worker performs the action
server-side, and the interaction is recorded. Same step format and same save path — only the
input channel differs.

**Event capture:** navigation (`framenavigated`), click (capture-phase listener, ignore
`preventDefault`ed scrolls), input/change (debounced 300 ms → one `fill`), select, checkbox/radio,
form submit (recorded only when it causes navigation). Duplicate suppression by
`(action, selector, value)` within 250 ms.

**Selector ladder (first match wins, all candidates stored for user override):**
`data-testid` → `data-test`, `data-cy`, `data-qa` → role+accessible name → label-for → placeholder →
stable `id` (rejecting hashes/numeric suffixes) → short CSS path (max 4 levels, no `:nth-child`
unless unavoidable) → `text=` (as last resort, flagged unstable). Steps saved with
`{selector, selector_candidates[], selector_strategy}` and a `stability` hint shown in the UI.

**UI contract:** status, target URL, live view, ordered step list with action/selector/description,
edit/delete/reorder, "Save as test case" (choose suite + name), "Discard".

## 10. Security model

- **Passwords:** bcrypt cost 12, min length 8 + strength check, never logged, never returned.
- **JWT:** HS256, secret from `JWT_SECRET` (startup fails if < 32 chars), claims
  `sub, role, iat, exp, iss, jti`, TTL `JWT_TTL` (default 24 h). 401 handled client-side by
  clearing the session and redirecting to `/login`. Logout is client-side (stateless) —
  documented as a known limitation (no server-side revocation list in v1).
- **Authorization:** every query is scoped by `owner_id` in the service layer; a missing or
  foreign resource returns **404**, never 403, to avoid ID enumeration.
- **Worker channel:** `/internal/v1/*` requires `X-Worker-Token` (constant-time compare) and is
  not routed through the public ingress; compose keeps both on the same private network.
- **SSRF awareness:** the recorder and `navigate` steps load arbitrary user URLs by design.
  Mitigation: block `file://`, `gopher://`, `data:` navigation, block cloud-metadata IPs
  (169.254.169.254, ::1, private ranges configurable via `WORKER_BLOCKED_HOSTS`), and run the
  browser as a non-root user in the container with no host mounts.
- **Uploads/screenshots:** ownership verified before streaming bytes; content-type forced to
  `image/png`; filenames never taken from user input.
- **Rate limiting:** per-IP token bucket on `/auth/login` and `/auth/register` (in-process,
  documented limitation for multi-replica).
- **Transport:** HTTPS terminated at the reverse proxy; `Secure`/`HttpOnly` cookies are not used
  (token in memory + `localStorage` with explicit trade-off note in the README).
- **CORS:** explicit allowlist from `CORS_ALLOWED_ORIGINS`, credentials disabled, methods/headers
  enumerated.
- **Secrets:** only via env (`.env` git-ignored, `.env.example` committed). No defaults for
  `JWT_SECRET` / `DB_PASSWORD` / `WORKER_SHARED_SECRET`.
- **Input hygiene:** all SQL goes through GORM parameter binding, `%w`-wrapped errors returned to
  clients are sanitized by the error mapper (never raw driver text).

## 11. Technical risks & dependencies

| # | Risk | Impact | Mitigation | Phase |
|---|---|---|---|---|
| R1 ✅ | ~~No Node.js / npm / container runtime on this machine~~ **Resolved 2026-11-22** — Node 22.23.5 (volta), Podman 6.2.0 + `podman-compose` 1.6.0, dev PostgreSQL 16.16 verified healthy with a real pgx connection from the host | none | closed; details in `DEVELOPMENT.md` | 0 |
| R2 | **Recorder interactivity** (frame streaming + input replay, IME, HiDPI scaling, latency) | highest-risk feature | Phase 5 starts with a time-boxed spike; Tier 2 assisted picker is a pre-approved fallback; recorder is isolated from the executor | 5 |
| R3 | Playwright ↔ browser-version drift inside containers | worker crashes on launch | pin `playwright` exactly and use the matching `mcr.microsoft.com/playwright:vX.Y.Z-jammy` image | 4 |
| R4 | Queue correctness (SKIP LOCKED, stale heartbeat, at-least-once) | duplicate or stuck executions | idempotent completion keyed by `job_id`, monotonic log `seq`, reaper with explicit tests | 4 |
| R5 | Long-running browser jobs leaking processes/zombies | memory growth, host exhaustion | `try/finally` browser close, per-job timeout, container memory limits, `--shm-size` in compose | 4 |
| R6 | Screenshot volume/disk growth | disk exhaustion | retention job (`SCREENSHOT_RETENTION_DAYS`), size caps, streaming (no base64 in DB) | 4/7 |
| R7 | JSONB schema drift between Go, TS and DB | runtime 422s or silent mis-execution | one documented contract (`WORKER-PROTOCOL.md`), zod validation in worker, enum validation in Go, contract tests | 3/4 |
| R8 | GORM ↔ hand-written migrations mismatch | runtime column errors | models are mappings only, integration test runs `migrate up` then exercises repos | 1 |
| R9 | Tailwind v4 tooling novelty | build breakage | pin versions; fall back to Tailwind 3.4 if the plugin misbehaves (decision recorded in DEVELOPMENT.md) | 6 |
| R10 | Suite execution duration vs HTTP timeouts | "hanging" runs | single long-poll claim endpoint, no synchronous execution endpoint, explicit per-step timeout budget | 4 |

**External dependencies:** PostgreSQL 16 (Podman container via `compose.yaml`, verified),
Node 22 LTS + Playwright browsers (Node installed; browsers arrive in Phase 4),
Go 1.25 (installed), Podman 6.2.0 + `podman-compose` 1.6.0 (the file is plain enough that
`docker compose` also works), `golang-migrate`, `swaggo/swag` (codegen).

**Sequencing dependencies:** Phase 4 needs the step schema from Phase 3; Phase 5 needs the browser
factory from Phase 4; Phase 6 needs live execution APIs; Phase 7 needs everything.

## 12. Phased implementation roadmap

Each phase ends with: builds green, tests run and reported verbatim, docs updated, `DEVELOPMENT.md`
progress row flipped. No phase starts before the previous phase's exit criteria are met.

| Phase | Deliverables | Exit criteria | Size |
|---|---|---|---|
| **0 Planning** ✅ | this document set | approval received | S |
| **1 Foundation** ✅ | repo scaffold, `cmd/api`, `cmd/migrate`, config, slog logger, GORM/pgx pool, `000001_init` migrations (all 12 tables), `/healthz` + `/readyz`, `apperr` + error middleware, request-id, graceful shutdown, `.env.example`, `.gitignore`, compose skeleton | **met 2026-11-22**: `go build`/`go vet`/`go test ./...` clean; `migrate up` → `down 1` → `up` verified on PostgreSQL 16; live `/healthz`, `/readyz`, 404/405 envelopes and DSN redaction verified (evidence in `DEVELOPMENT.md`) | M |
| **2 Auth** | bcrypt, register/login/me/patch, JWT issue+verify, auth middleware, role claim, login rate limit, unit tests | health + auth flows exercised with `curl`; service tests pass; no secret in logs | S |
| **3 Domain CRUD** | projects, environments, suites, test cases, steps (create/list/get/update/delete/duplicate/reorder), pagination/filter/sort, ownership scoping, Swagger annotations, repo+service tests | every endpoint returns the documented envelope; reorder is transactional and idempotent; Swagger UI renders; tests pass | L |
| **4 Execution engine** | `workerclient` adapter, `execution_jobs` queue + dispatcher + reaper, `/internal/v1` endpoints, WS hub + tickets, worker runner (browser factory, 14 actions, timeouts, cancellation, cleanup), screenshots pipeline, `POST/GET /executions` | worker executes a real 10-step scenario on Chromium against a fixture site; `/executions/:id` + WS show live progress; cancel stops within 10 s; no lingering browser after failure; integration tests pass | L |
| **5 Recorder** | recorder session API + worker recorder module, selector ladder, event capture/serialization, live frame relay, save-as-test-case, tests for capture + serialization | record a login flow end-to-end, edit/reorder steps, save, then execute the saved case successfully | L |
| **6 Frontend** | Vite/TS strict/Tailwind/Pinia/Router/Axios foundation, auth store + guards, app shell (sidebar/header/theme), UI kit, Dashboard, Projects, Suites/Cases editor, Recorder, Executions live view, Reports, Settings, toasts/empty/error states | all pages hit real APIs (no mock data), responsive, `vue-tsc --noEmit` clean, `vitest` component tests pass | L |
| **7 Stabilization** | full integration run, E2E suite (Playwright driving the real app), security/perf review, cleanup of dead code, README/API docs/dev guide/test guide, final report | every checklist item from Prompt 7 verified with evidence; final summary lists implemented features, real test results, known limitations, next steps | M |

Frontend work (Phase 6) is intentionally started **after** Phases 2–5 are functional so it is built
against real APIs instead of placeholders; the layout/UI kit can be brought forward if preferred.

## 13. Definition of done (per module)

1. `go build ./...`, `go vet ./...`, `go test ./...` (or `vue-tsc`, `vitest`, `npm run build`) pass —
   output quoted in the session, never assumed.
2. Request/response contracts match this document and Swagger.
3. Errors mapped to typed codes with correct HTTP status; no internal detail leaked.
4. Ownership/authorization enforced in the service layer, verified by a test.
5. Migrations added for any schema change, never edited retroactively.
6. `DEVELOPMENT.md` and, if contracts changed, `docs/*.md` updated in the same change.
7. No secret, credential or absolute local path committed.

## 14. Open questions (remaining; non-blocking)

1. **Recorder tier** — approve Tier 1 streamed interactive recording with Tier 2 fallback, or
   commit to Tier 2 only for v1?
2. **Auth depth** — access token only (24 h, client-side logout) as implemented by default, or
   refresh tokens + server-side revocation in v1?
3. **Project sharing** — owner-only (as designed) or add `project_members` now?

Resolved:
- ~~Go module path~~ — `github.com/ariskusnulwiditama/golang-webtest`.
- ~~Toolchain install~~ — Node 22.23.5 and Podman 6.2.0 are installed; no Docker Desktop needed.
- ~~Container runtime / compose provider~~ — `podman-compose` 1.6.0 installed; `podman compose` works.
- ~~Database engine~~ — PostgreSQL 16 confirmed. SQLite was evaluated and rejected (single writer
  would serialise job claims, plus `jsonb`/`citext`/`text[]`/deferrable constraints are all used).
