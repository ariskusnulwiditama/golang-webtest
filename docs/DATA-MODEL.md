# WebTest Studio — Database Schema

PostgreSQL 16. Every primary key is `uuid` (`gen_random_uuid()`, built into PG13+).
All timestamps are `timestamptz` in UTC. No soft deletes: hard delete + cascade, because
executions are immutable history and screenshots are files, not rows.

The schema is owned by SQL migrations (`migrations/*.sql`), never by GORM AutoMigrate.

## 1. Relationship overview

```
users 1─┬─< projects 1─┬─< test_suites 1──< test_cases 1──< test_steps
        │              ├─< environments
        │              └─< test_executions ──┬──< execution_results
        └─< test_executions (requested_by)   ├──< execution_logs
                                             └──< test_screenshots
```

## 2. Business tables

### users
| column | type | constraints |
|---|---|---|
| id | uuid | pk default gen_random_uuid() |
| email | citext | not null, unique (case-insensitive without a functional index) |
| password_hash | text | not null (bcrypt cost 12) |
| name | varchar(120) | not null |
| role | varchar(20) | not null default 'user', check in ('admin','user') |
| is_active | boolean | not null default true |
| last_login_at | timestamptz | null |
| created_at / updated_at | timestamptz | not null default now() |

Index: unique `users_email_key`. Extension: `citext` (created by migration 000001).

### projects
| column | type | constraints |
|---|---|---|
| id | uuid | pk |
| owner_id | uuid | not null fk users(id) on delete cascade |
| name | varchar(150) | not null |
| description | text | null |
| created_at / updated_at | timestamptz | not null |

Indexes: unique `(owner_id, lower(name))`, `(owner_id, created_at desc)`.
All reads are scoped by `owner_id` (or admin) in the service layer, backed by these indexes.

### test_suites
| column | type | constraints |
|---|---|---|
| id | uuid | pk |
| project_id | uuid | not null fk projects(id) on delete cascade |
| name | varchar(150) | not null |
| description | text | null |
| position | integer | not null default 0 |
| created_at / updated_at | timestamptz | not null |

Indexes: unique `(project_id, lower(name))`, `(project_id, position)`.

### test_cases
| column | type | constraints |
|---|---|---|
| id | uuid | pk |
| suite_id | uuid | not null fk test_suites(id) on delete cascade |
| name | varchar(200) | not null |
| description | text | null |
| base_url | text | null (overrides environment base_url when set) |
| priority | varchar(10) | not null default 'medium', check in ('low','medium','high','critical') |
| tags | text[] | not null default '{}' |
| is_enabled | boolean | not null default true |
| position | integer | not null default 0 |
| created_by | uuid | null fk users(id) on delete set null |
| created_at / updated_at | timestamptz | not null |

Indexes: `(suite_id, position)`, GIN `(tags)`, `(created_by)`.
`project_id` is deliberately **not** denormalised: "test cases of a project" joins through
`test_suites` (PK index, negligible cost) so the project can never drift out of sync.

### test_steps
| column | type | constraints |
|---|---|---|
| id | uuid | pk |
| test_case_id | uuid | not null fk test_cases(id) on delete cascade |
| position | integer | not null |
| action | varchar(24) | not null, check in the 14 supported actions |
| config | jsonb | not null default '{}' — action-specific parameters |
| description | text | null |
| created_at / updated_at | timestamptz | not null |

Constraint: `unique (test_case_id, position) DEFERRABLE INITIALLY DEFERRED` — allows a
reorder to run as one `UPDATE` inside a transaction without intermediate collisions.
Index: `(test_case_id, position)`.

`action` is a real column (constrained, sortable, filterable) and `config` holds **only**
action-specific fields — the action is not duplicated inside the JSON.

### environments
| column | type | constraints |
|---|---|---|
| id | uuid | pk |
| project_id | uuid | not null fk projects(id) on delete cascade |
| name | varchar(100) | not null |
| base_url | text | not null |
| variables | jsonb | not null default '{}' |
| headers | jsonb | not null default '{}' |
| is_default | boolean | not null default false |
| created_at / updated_at | timestamptz | not null |

Indexes: unique `(project_id, lower(name))`, partial unique `(project_id) where is_default`
(at most one default per project).

### test_executions
| column | type | constraints |
|---|---|---|
| id | uuid | pk |
| project_id | uuid | not null fk projects(id) on delete cascade |
| suite_id | uuid | null fk test_suites(id) on delete set null |
| test_case_id | uuid | null fk test_cases(id) on delete set null |
| environment_id | uuid | null fk environments(id) on delete set null |
| requested_by | uuid | null fk users(id) on delete set null |
| status | varchar(16) | not null default 'queued', check in ('queued','running','passed','failed','cancelled','timeout','error') |
| browser | varchar(16) | not null, check in ('chromium','firefox','webkit') |
| headless | boolean | not null default true |
| trigger | varchar(16) | not null default 'manual', check in ('manual','api') |
| total_steps / passed_steps / failed_steps / skipped_steps | integer | not null default 0 |
| duration_ms | integer | null |
| started_at / finished_at | timestamptz | null |
| error_message | text | null |
| options | jsonb | not null default '{}' (step timeout, viewport, retries) |
| cancel_requested_at | timestamptz | null |
| created_at / updated_at | timestamptz | not null |

Constraint: `check ((suite_id is not null) <> (test_case_id is not null))` — exactly one target.
Indexes: `(project_id, created_at desc)`, `(status, created_at)`, `(requested_by, created_at desc)`.
`*_steps` counters are the rollup across all `execution_results` of the execution.

### execution_results — one row per test case run
| column | type | constraints |
|---|---|---|
| id | uuid | pk |
| execution_id | uuid | not null fk test_executions(id) on delete cascade |
| test_case_id | uuid | null fk test_cases(id) on delete set null |
| test_case_name | varchar(200) | not null (snapshot: renamed/deleted cases still read correctly) |
| status | varchar(16) | not null, same value domain as execution.status |
| total_steps / passed_steps / failed_steps / skipped_steps | integer | not null default 0 |
| duration_ms | integer | null |
| error_message | text | null |
| steps | jsonb | not null default '[]' — ordered per-step result snapshot |
| started_at / finished_at | timestamptz | null |
| created_at | timestamptz | not null |

Indexes: `(execution_id)`, `(test_case_id, status)`, `(status)`.

**Decision:** per-step results live in `steps jsonb`, not in an 11th table. They are an
immutable snapshot, are always read together with their parent result, and are never
queried individually. Keeps the spec's table list intact and avoids write amplification
(a 200-step suite would otherwise produce thousands of rows per run).

### execution_logs
| column | type | constraints |
|---|---|---|
| id | uuid | pk |
| execution_id | uuid | not null fk test_executions(id) on delete cascade |
| result_id | uuid | null fk execution_results(id) on delete cascade |
| step_id | uuid | null (logical, **no FK**) |
| seq | integer | not null (monotonic per execution; worker idempotency key) |
| level | varchar(8) | not null check in ('debug','info','warn','error') |
| message | text | not null |
| context | jsonb | not null default '{}' |
| created_at | timestamptz | not null |

Indexes: unique `(execution_id, seq)`, `(execution_id, level)`.
No FK on `step_id` on purpose: steps are editable/deletable, while logs must survive as
history (the `steps` snapshot in `execution_results` keeps them interpretable).
Volume control: worker batches (50 lines / 500 ms); retention job deletes after N days.

### test_screenshots
| column | type | constraints |
|---|---|---|
| id | uuid | pk |
| execution_id | uuid | not null fk test_executions(id) on delete cascade |
| result_id | uuid | null fk execution_results(id) on delete cascade |
| step_id | uuid | null (logical, no FK) |
| name | varchar(120) | not null |
| storage_key | text | not null (path/object key, never a URL) |
| mime_type | varchar(64) | not null default 'image/png' |
| size_bytes | integer | not null |
| width / height | integer | null |
| captured_at | timestamptz | not null |
| created_at | timestamptz | not null |

Indexes: unique `(execution_id, storage_key)`, `(execution_id)`, `(result_id)`.
Bytes are never stored in the DB. `GET /api/v1/screenshots/:id` streams from
`storage.Storage` after an ownership check on the parent execution.

## 3. Infrastructure tables (not business modules)

### execution_jobs — the Go⇄Node protocol backbone
| column | type | notes |
|---|---|---|
| id | uuid | pk = **job id** |
| execution_id | uuid | null fk test_executions(id) on delete cascade (null for recorder jobs) |
| kind | varchar(24) | `run_test` \| `record_session` |
| status | varchar(16) | `queued`\|`claimed`\|`running`\|`completed`\|`failed`\|`cancelled`\|`timeout` |
| payload | jsonb | immutable payload handed to the worker |
| result | jsonb | final result envelope (audit/debug) |
| attempts | smallint | not null default 0 |
| worker_id | varchar(64) | null |
| claimed_at / last_heartbeat_at / finished_at | timestamptz | null |
| cancel_requested_at | timestamptz | null |
| error_message | text | null |
| created_at / updated_at | timestamptz | not null |

Indexes: partial `(status, created_at) where status='queued'` (claim path),
`(status, last_heartbeat_at)` (reaper path), `(execution_id)`.

### ws_tickets — short-lived WebSocket handshake tokens
| column | type | notes |
|---|---|---|
| id | uuid | pk |
| user_id | uuid | not null fk users(id) on delete cascade |
| token_hash | bytea | not null unique (SHA-256 of the opaque ticket) |
| scope | varchar(120) | not null, e.g. `execution:<uuid>` or `recorder:<uuid>` |
| expires_at | timestamptz | not null (now() + 60 s) |
| used_at | timestamptz | null (single use) |
| created_at | timestamptz | not null |

Index: `(expires_at)`. Cleanup piggybacks on the reaper goroutine.
Rationale: browsers cannot set `Authorization` on a `WebSocket` handshake, and putting a long-lived
JWT in a query string leaks it into access logs, `Referer`, and history.

## 4. Integrity & performance notes

- All child tables cascade from their parent so deleting a project removes suites → cases → steps
  and executions → results/logs/screenshots in one statement.
- Deleting a suite/test case keeps historical executions readable: `test_executions.suite_id` /
  `test_case_id` are `ON DELETE SET NULL` and `execution_results.test_case_name` is a snapshot.
- Counters (`total_steps`, `passed_steps`, …) are written once at finalization, so dashboards and
  reports read pre-aggregated columns instead of scanning `jsonb`.
- Dashboard/report queries hit `(project_id, created_at desc)` and `(status)` indexes; no `OFFSET`
  beyond `page_size * 100` (deep pages are blocked by pagination caps).
- JSONB columns are validated in Go/TS on write; GIN indexes are added only where a filter is
  actually exposed (`test_cases.tags`).

## 5. Migration strategy

- Files: `migrations/000001_init.up.sql` / `.down.sql`, then one numbered pair per change.
  Existing migration files are never edited after being applied anywhere.
- `cmd/migrate` subcommands: `up`, `down <n>`, `status` (golang-migrate + `iofs` source + `embed.FS`).
- `DB_AUTO_MIGRATE=true` (dev/compose only) runs `up` before the server starts; production runs
  `cmd/migrate` as a separate step/entrypoint stage.
- `000001_init.down.sql` drops everything in reverse dependency order (dev/test only).
- Seeds are separate (`migrations/seed_dev.sql`, executed manually) and are not migrations.
