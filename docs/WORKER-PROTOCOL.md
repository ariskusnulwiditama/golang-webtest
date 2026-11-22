# WebTest Studio — Go ⇄ Node Worker Protocol (v1)

Contract between the Go API (employer) and the Node.js Playwright worker (executor).
Both sides must change together; the TypeScript zod schemas in
`worker/src/protocol/schemas.ts` and the Go structs in `internal/workerclient/dto.go` are the
executable form of this document (contract tests keep them aligned).

## 1. Transport

- Base URL: `WORKER_URL` (default `http://worker:8090` internal, `http://localhost:8090` dev).
- Auth: `X-Worker-Token: $WORKER_SHARED_SECRET` on every call, constant-time compared.
  Missing/invalid → `401` and the request is dropped without side effects.
- Content type: `application/json` (screenshots use `multipart/form-data`).
- All timestamps are RFC 3339 UTC (`2026-11-22T10:15:30Z`); durations are integer milliseconds.
- Model: **pull-based job queue in PostgreSQL**. The worker never receives inbound connections
  from the internet and the API never pushes work; the API owns all state transitions.

## 2. Lifecycle

```
queued ──claim──► claimed ──first heartbeat──► running ──┐
   │                  │                                  ├─► completed
   │                  └──stale (>60s, no heartbeat)──┐   ├─► failed
   └──cancel_requested (before claim)──► cancelled   └──►├─► cancelled
                                                         └─► timeout
```

- Every transition is written to `execution_jobs` and mirrored to `test_executions`.
- `complete` / `fail` are **idempotent**: a duplicate call returns `200` with the stored result
  and does not mutate state (`job_id` is the idempotency key).
- `attempts` increments on claim; if `attempts > JOB_MAX_ATTEMPTS` the job is finalized as
  `failed` instead of being claimed again.

## 3. `POST /internal/v1/jobs/claim`

Request (long-poll, held up to 20 s when the queue is empty; returns `204` on timeout):

```json
{ "worker_id": "worker-local-1", "capacity": 2, "kinds": ["run_test", "record_session"] }
```

Response `200`:

```json
{
  "job_id": "6f0c...",
  "kind": "run_test",
  "execution_id": "b1a2...",
  "lease_expires_at": "2026-11-22T10:16:00Z",
  "heartbeat_interval_ms": 5000,
  "cancel_grace_ms": 10000,
  "payload": { "...": "see §5" }
}
```

Claims are atomic (`UPDATE ... FOR UPDATE SKIP LOCKED`), so N workers never take the same job.
`capacity` is the number of jobs the worker can run concurrently; a job occupies exactly one slot
and one browser context for its whole life — contexts are never shared between jobs.

## 4. Job control endpoints

| Endpoint | Request | Response |
|---|---|---|
| `POST /jobs/:id/heartbeat` | `{"state":"running","current_step":3,"progress":0.4}` | `{"cancel":false,"heartbeat_interval_ms":5000}` |
| `POST /jobs/:id/state` | - | `{"status":"running","cancel":true}` (used before long waits, optional) |
| `POST /jobs/:id/logs` | `{"logs":[{"seq":12,"level":"info","message":"...","step_id":"...","context":{}}]}` | `{"accepted":1,"duplicate":0}` |
| `POST /jobs/:id/screenshots` | multipart: `meta` (JSON) + `file` (PNG) | `{"screenshot_id":"...","storage_key":"..."}` |
| `POST /jobs/:id/complete` | `{"status":"passed","result":{...}}` | `{"execution_id":"...","status":"passed"}` |
| `POST /jobs/:id/fail` | `{"reason":"browser_crashed","message":"...","logs":[...]}` | `{"execution_id":"...","status":"error"}` |

Rules:

- `seq` must be strictly increasing per execution; replays/duplicates are ignored, never an error.
- Cancellation is cooperative: the API sets the flag, the next heartbeat (≤ 5 s) or `/state` call
  returns `cancel: true`, the worker aborts the current step, closes the context, and calls
  `complete` with `status: "cancelled"`. If the worker does not acknowledge within `cancel_grace_ms`,
  the API finalizes as `cancelled`/`timeout` itself.
- Any `5xx` from the API on control calls is retried by the worker with exponential backoff
  (100 ms → 5 s, 5 attempts, respecting `lease_expires_at`).

## 5. `run_test` payload

```json
{
  "execution_id": "b1a2...",
  "project_id": "c3d4...",
  "browser": "chromium",
  "headless": true,
  "base_url": "https://demo.example.test",
  "variables": { "user": "qa1", "pass": "from-env" },
  "headers": { "X-Test-Run": "true" },
  "options": {
    "step_timeout_ms": 15000,
    "navigation_timeout_ms": 30000,
    "execution_timeout_ms": 600000,
    "viewport": { "width": 1440, "height": 900 },
    "screenshot_on_failure": true,
    "stop_on_first_failure": true,
    "retries": 0
  },
  "test_cases": [
    {
      "result_id": "d5e6...",
      "test_case_id": "a7b8...",
      "name": "Login flow",
      "base_url": "https://demo.example.test",
      "steps": [
        { "id": "s1", "position": 1, "action": "navigate",
          "config": { "url": "{{base_url}}/login" }, "description": "Open login" },
        { "id": "s2", "position": 2, "action": "fill",
          "config": { "selector": "[data-testid=email]", "value": "{{user}}", "clear": true },
          "description": "Fill email" }
      ]
    }
  ]
}
```

`{{var}}` interpolation happens **in the worker** (env variables + `variables` + `base_url`),
so the stored step JSON stays environment-agnostic.

## 6. Step config contract (14 actions)

Each object below is the `config` value stored in `test_steps.config`; `action` is the sibling column.

| action | config fields (bold = required) | notes |
|---|---|---|
| `navigate` | **url** | `{{vars}}` allowed; waits for `load` |
| `click` | **selector** | auto-waits for visible+enabled, then clicks |
| `fill` | **selector**, **value**, `clear`(true) | uses `locator.fill` |
| `type` | **selector**, **text**, `delay_ms`(0) | keystroke-level typing |
| `select` | **selector**, one of **value** \| `label` \| `index` | exactly one selector mode |
| `check` / `uncheck` | **selector** | verifies the resulting state |
| `wait` | **selector**, `state`(visible), `timeout_ms` | explicit wait, never a fixed sleep |
| `screenshot` | `name`, `full_page`(false) | returns a screenshot reference |
| `assert_text` | **selector**, **expected**, `match`(contains) | fails with actual vs expected |
| `assert_visible` | **selector**, `timeout_ms` | fails if not visible in time |
| `assert_url` | **expected**, `match`(contains) | compared against `page.url()` |
| `assert_title` | **expected**, `match`(contains) | compared against `page.title()` |

`match` accepts `contains` or `equals`. Validation happens twice: Go on write
(`422 VALIDATION_ERROR`) and worker via zod (`failed` with `invalid_payload`). Unknown action is
rejected; unknown extra fields are ignored with a `warn` log (forward compatibility for newer workers).

## 7. Result envelope (`POST /jobs/:id/complete`)

```json
{
  "status": "failed",
  "result": {
    "execution_id": "b1a2...",
    "status": "failed",
    "browser": "chromium",
    "started_at": "2026-11-22T10:15:30Z",
    "finished_at": "2026-11-22T10:16:02Z",
    "duration_ms": 32000,
    "totals": { "total": 12, "passed": 10, "failed": 1, "skipped": 1 },
    "results": [
      {
        "result_id": "d5e6...", "test_case_id": "a7b8...", "test_case_name": "Login flow",
        "status": "failed",
        "started_at": "2026-11-22T10:15:31Z", "finished_at": "2026-11-22T10:16:02Z",
        "duration_ms": 31000,
        "error_message": "assert_text failed: expected \"Welcome\" got \"Sign in\"",
        "steps": [
          { "step_id": "s1", "position": 1, "action": "navigate", "description": "Open login",
            "status": "passed",
            "started_at": "2026-11-22T10:15:31.120Z", "finished_at": "2026-11-22T10:15:32.480Z",
            "duration_ms": 1360, "error_message": null, "screenshot_ids": [] },
          { "step_id": "s2", "position": 2, "action": "assert_text", "description": "Greeting",
            "status": "failed",
            "started_at": "2026-11-22T10:16:01.000Z", "finished_at": "2026-11-22T10:16:02.000Z",
            "duration_ms": 1000,
            "error_message": "assert_text failed: expected \"Welcome\" got \"Sign in\"",
            "screenshot_ids": ["9f8e..."] }
        ]
      }
    ],
    "screenshots": [
      { "id": "9f8e...", "step_id": "s2", "result_id": "d5e6...", "name": "failure-step-2",
        "storage_key": "b1a2/d5e6/s2.png", "width": 1440, "height": 900,
        "captured_at": "2026-11-22T10:16:02Z" }
    ],
    "logs_uploaded": 34,
    "worker": { "id": "worker-local-1", "version": "1.0.0", "playwright": "1.x.y" }
  }
}
```

Every step always reports `step_id`, `description`, `status`, `started_at`, `finished_at`,
`duration_ms` and `error_message` (null when passed). After a failure the remaining steps are
reported as `skipped` when `stop_on_first_failure` is true (default); otherwise execution continues
and the case is `failed` if any step failed.

## 8. Recorder session protocol

Separate from `run_test` (own job kind, own endpoints, own module — not part of the executor):

1. SPA → `POST /api/v1/recorder/sessions` → API creates `execution_jobs(kind=record_session)` with
   `{session_id, url, browser, viewport}` and returns `{session_id, status:"starting"}`.
2. Worker claims the job, launches the browser, starts the screencast, then streams
   `POST /internal/v1/recorder/:id/frame` `{seq, ts, mime:"image/jpeg", data:<base64>}` (~10 fps).
3. Worker long-polls `GET /internal/v1/recorder/:id/commands?after_seq=` (≤ 1 s) for input events
   `{seq, type:"mouse"|"key"|"scroll"|"navigate", payload}` and replays them via CDP
   (`Input.dispatchMouseEvent`, `Input.insertText`, `Input.dispatchKeyEvent`).
4. Worker batch-posts captured interactions to `POST /internal/v1/recorder/:id/event`
   `{seq, events:[{action, selector, selector_candidates, value, description, ts}]}`. The API
   converts them to draft steps and pushes them over WS to the SPA.
5. `POST /api/v1/recorder/sessions/:id/stop` → API sets the stop flag → worker closes the browser,
   posts final events, completes the job → API returns the ordered draft steps.
6. Saving is a **pure API operation** (`/recorder/sessions/:id/save`) that creates a `test_case` +
   `test_steps` from the (possibly user-edited) steps — no worker involvement.

Sessions auto-expire after `RECORDER_MAX_DURATION_MS` (default 10 min). The reaper force-finalizes
them and kills the browser through the same cancellation path as jobs.

## 9. Error codes (both directions)

| code | meaning | expected reaction |
|---|---|---|
| `unauthorized` | bad/missing `X-Worker-Token` | log and stop (configuration error) |
| `job_not_found` | unknown job id | log and drop |
| `invalid_state` | complete/fail on a finalized job | treat as success (idempotent) |
| `lease_expired` | job reclaimed by the reaper | abort immediately, stop calling back |
| `invalid_payload` | zod validation failed | report `fail` with the validation detail |
| `browser_launch_failed` | browser could not start | `fail`, clean up, no in-job retry |
| `timeout` | budget exceeded | `complete` with `status: "timeout"` |
| `cancelled` | cancel acknowledged | `complete` with `status: "cancelled"` |

Worker retries only transient control-plane failures (`5xx`, connection reset) with exponential
backoff (100 ms → 5 s, max 5 attempts) while the lease is still valid. It never retries a failed
step by itself unless `options.retries > 0`.

## 10. Configuration (env)

| Variable | Default | Used by |
|---|---|---|
| `WORKER_URL` | `http://worker:8090` | API |
| `WORKER_SHARED_SECRET` | — (required) | both |
| `WORKER_ID` | `worker-local` | worker |
| `WORKER_PORT` | `8090` | worker |
| `WORKER_CONCURRENCY` | `2` | worker |
| `WORKER_BROWSERS_PATH` | image default | worker |
| `WORKER_BLOCKED_HOSTS` | `file,data,gopher,169.254.169.254` | worker |
| `JOB_CLAIM_LONG_POLL_MS` | `20000` | both |
| `JOB_HEARTBEAT_MS` | `5000` | both |
| `JOB_STALE_AFTER_MS` | `60000` | API |
| `JOB_MAX_ATTEMPTS` | `1` | API |
| `RECORDER_MAX_DURATION_MS` | `600000` | both |
| `RECORDER_FRAME_FPS` | `10` | worker |
