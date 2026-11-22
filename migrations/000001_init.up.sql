-- 000001_init — initial WebTest Studio schema (PostgreSQL 16).
--
-- Owned by migrations, never by GORM AutoMigrate. Mirrors docs/DATA-MODEL.md:
-- 10 business tables + execution_jobs + ws_tickets, UUID primary keys,
-- timestamptz in UTC, hard deletes with cascade.
--
-- gen_random_uuid() is built into PostgreSQL >= 13, so only citext is needed.

CREATE EXTENSION IF NOT EXISTS citext;

-- ---------------------------------------------------------------- users ----
CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email         citext      NOT NULL,
    password_hash text        NOT NULL,
    name          varchar(120) NOT NULL,
    role          varchar(20) NOT NULL DEFAULT 'user'
                  CONSTRAINT users_role_check CHECK (role IN ('admin', 'user')),
    is_active     boolean     NOT NULL DEFAULT true,
    last_login_at timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT users_email_key UNIQUE (email)
);

-- ------------------------------------------------------------- projects ----
CREATE TABLE projects (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name        varchar(150) NOT NULL,
    description text,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- Case-insensitive project name per owner.
CREATE UNIQUE INDEX projects_owner_name_key ON projects (owner_id, lower(name));
CREATE INDEX projects_owner_created_idx ON projects (owner_id, created_at DESC);

-- ---------------------------------------------------------- test_suites ----
CREATE TABLE test_suites (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name        varchar(150) NOT NULL,
    description text,
    position    integer     NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX test_suites_project_name_key ON test_suites (project_id, lower(name));
CREATE INDEX test_suites_project_position_idx ON test_suites (project_id, position);

-- ----------------------------------------------------------- test_cases ----
-- No project_id on purpose: "cases of a project" joins through test_suites so
-- the two can never drift apart (docs/DATA-MODEL.md §2).
CREATE TABLE test_cases (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    suite_id    uuid NOT NULL REFERENCES test_suites (id) ON DELETE CASCADE,
    name        varchar(200) NOT NULL,
    description text,
    base_url    text,
    priority    varchar(10) NOT NULL DEFAULT 'medium'
                CONSTRAINT test_cases_priority_check CHECK (priority IN ('low', 'medium', 'high', 'critical')),
    tags        text[]      NOT NULL DEFAULT '{}',
    is_enabled  boolean     NOT NULL DEFAULT true,
    position    integer     NOT NULL DEFAULT 0,
    created_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX test_cases_suite_position_idx ON test_cases (suite_id, position);
CREATE INDEX test_cases_tags_idx ON test_cases USING gin (tags);
CREATE INDEX test_cases_created_by_idx ON test_cases (created_by);

-- ----------------------------------------------------------- test_steps ----
-- `action` is a real column (constrained, sortable, filterable); `config` holds
-- only action-specific fields. The 14 actions come from WORKER-PROTOCOL.md §6.
CREATE TABLE test_steps (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    test_case_id uuid NOT NULL REFERENCES test_cases (id) ON DELETE CASCADE,
    position     integer NOT NULL,
    action       varchar(24) NOT NULL
                 CONSTRAINT test_steps_action_check CHECK (action IN (
                     'navigate', 'click', 'fill', 'type', 'select', 'check', 'uncheck',
                     'hover', 'wait', 'screenshot',
                     'assert_text', 'assert_visible', 'assert_url', 'assert_title'
                 )),
    config       jsonb       NOT NULL DEFAULT '{}'::jsonb,
    description  text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),

    -- DEFERRABLE so a reorder can run as a single UPDATE inside one transaction
    -- without tripping over intermediate collisions.
    CONSTRAINT test_steps_case_position_key UNIQUE (test_case_id, position)
        DEFERRABLE INITIALLY DEFERRED
);

CREATE INDEX test_steps_case_position_idx ON test_steps (test_case_id, position);

-- --------------------------------------------------------- environments ----
CREATE TABLE environments (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name       varchar(100) NOT NULL,
    base_url   text        NOT NULL,
    variables  jsonb       NOT NULL DEFAULT '{}'::jsonb,
    headers    jsonb       NOT NULL DEFAULT '{}'::jsonb,
    is_default boolean     NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX environments_project_name_key ON environments (project_id, lower(name));
-- At most one default environment per project.
CREATE UNIQUE INDEX environments_one_default_per_project_idx
    ON environments (project_id) WHERE is_default;

-- ------------------------------------------------------ test_executions ----
CREATE TABLE test_executions (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    -- History survives deletion of the suite/case: NULL + name snapshot in results.
    suite_id        uuid REFERENCES test_suites (id) ON DELETE SET NULL,
    test_case_id    uuid REFERENCES test_cases (id) ON DELETE SET NULL,
    environment_id  uuid REFERENCES environments (id) ON DELETE SET NULL,
    requested_by    uuid REFERENCES users (id) ON DELETE SET NULL,
    status          varchar(16) NOT NULL DEFAULT 'queued'
                    CONSTRAINT test_executions_status_check CHECK (status IN (
                        'queued', 'running', 'passed', 'failed', 'cancelled', 'timeout', 'error')),
    browser         varchar(16) NOT NULL
                    CONSTRAINT test_executions_browser_check CHECK (browser IN ('chromium', 'firefox', 'webkit')),
    headless        boolean     NOT NULL DEFAULT true,
    trigger         varchar(16) NOT NULL DEFAULT 'manual'
                    CONSTRAINT test_executions_trigger_check CHECK (trigger IN ('manual', 'api')),
    total_steps     integer     NOT NULL DEFAULT 0,
    passed_steps    integer     NOT NULL DEFAULT 0,
    failed_steps    integer     NOT NULL DEFAULT 0,
    skipped_steps   integer     NOT NULL DEFAULT 0,
    duration_ms     integer,
    started_at      timestamptz,
    finished_at     timestamptz,
    error_message   text,
    options         jsonb       NOT NULL DEFAULT '{}'::jsonb,
    cancel_requested_at timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    -- Exactly one target: a whole suite or a single test case.
    CONSTRAINT test_executions_one_target_check
        CHECK ((suite_id IS NOT NULL) <> (test_case_id IS NOT NULL))
);

CREATE INDEX test_executions_project_created_idx ON test_executions (project_id, created_at DESC);
CREATE INDEX test_executions_status_created_idx ON test_executions (status, created_at);
CREATE INDEX test_executions_requested_by_created_idx ON test_executions (requested_by, created_at DESC);

-- ----------------------------------------------------- execution_results ----
-- One row per test case run. Per-step detail lives in `steps` jsonb: it is an
-- immutable snapshot always read with its parent, never queried on its own.
CREATE TABLE execution_results (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    execution_id    uuid NOT NULL REFERENCES test_executions (id) ON DELETE CASCADE,
    test_case_id    uuid REFERENCES test_cases (id) ON DELETE SET NULL,
    -- Snapshot so renamed or deleted cases still read correctly.
    test_case_name  varchar(200) NOT NULL,
    status          varchar(16) NOT NULL
                    CONSTRAINT execution_results_status_check CHECK (status IN (
                        'queued', 'running', 'passed', 'failed', 'cancelled', 'timeout', 'error')),
    total_steps     integer     NOT NULL DEFAULT 0,
    passed_steps    integer     NOT NULL DEFAULT 0,
    failed_steps    integer     NOT NULL DEFAULT 0,
    skipped_steps   integer     NOT NULL DEFAULT 0,
    duration_ms     integer,
    error_message   text,
    steps           jsonb       NOT NULL DEFAULT '[]'::jsonb,
    started_at      timestamptz,
    finished_at     timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX execution_results_execution_idx ON execution_results (execution_id);
CREATE INDEX execution_results_case_status_idx ON execution_results (test_case_id, status);
CREATE INDEX execution_results_status_idx ON execution_results (status);

-- -------------------------------------------------------- execution_logs ----
CREATE TABLE execution_logs (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    execution_id uuid NOT NULL REFERENCES test_executions (id) ON DELETE CASCADE,
    result_id    uuid REFERENCES execution_results (id) ON DELETE CASCADE,
    -- Logical reference only: steps are editable, logs are history and must
    -- survive step deletion (the steps snapshot keeps them interpretable).
    step_id      uuid,
    seq          integer NOT NULL,
    level        varchar(8) NOT NULL
                 CONSTRAINT execution_logs_level_check CHECK (level IN ('debug', 'info', 'warn', 'error')),
    message      text    NOT NULL,
    context      jsonb   NOT NULL DEFAULT '{}'::jsonb,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- (execution_id, seq) is the worker's idempotency key: replays are ignored.
CREATE UNIQUE INDEX execution_logs_execution_seq_key ON execution_logs (execution_id, seq);
CREATE INDEX execution_logs_execution_level_idx ON execution_logs (execution_id, level);

-- ----------------------------------------------------- test_screenshots ----
CREATE TABLE test_screenshots (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    execution_id uuid NOT NULL REFERENCES test_executions (id) ON DELETE CASCADE,
    result_id    uuid REFERENCES execution_results (id) ON DELETE CASCADE,
    step_id      uuid,
    name         varchar(120) NOT NULL,
    -- Path or object key, never a URL: access always goes through the API.
    storage_key  text    NOT NULL,
    mime_type    varchar(64) NOT NULL DEFAULT 'image/png',
    size_bytes   integer NOT NULL,
    width        integer,
    height       integer,
    captured_at  timestamptz NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT test_screenshots_size_check CHECK (size_bytes >= 0)
);

CREATE UNIQUE INDEX test_screenshots_execution_storage_key_key
    ON test_screenshots (execution_id, storage_key);
CREATE INDEX test_screenshots_execution_idx ON test_screenshots (execution_id);
CREATE INDEX test_screenshots_result_idx ON test_screenshots (result_id);

-- ------------------------------------------------------- execution_jobs ----
-- The Go <-> Node protocol backbone (docs/WORKER-PROTOCOL.md). It doubles as the
-- queue: workers claim with FOR UPDATE SKIP LOCKED, so no broker is needed.
CREATE TABLE execution_jobs (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- NULL for recorder jobs, which belong to a session rather than an execution.
    execution_id  uuid REFERENCES test_executions (id) ON DELETE CASCADE,
    kind          varchar(24) NOT NULL
                  CONSTRAINT execution_jobs_kind_check CHECK (kind IN ('run_test', 'record_session')),
    status        varchar(16) NOT NULL DEFAULT 'queued'
                  CONSTRAINT execution_jobs_status_check CHECK (status IN (
                      'queued', 'claimed', 'running', 'completed', 'failed', 'cancelled', 'timeout')),
    payload       jsonb     NOT NULL,
    result        jsonb,
    attempts      smallint  NOT NULL DEFAULT 0,
    worker_id     varchar(64),
    claimed_at    timestamptz,
    last_heartbeat_at timestamptz,
    finished_at   timestamptz,
    cancel_requested_at timestamptz,
    error_message text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- Claim path: only queued jobs are scanned, oldest first.
CREATE INDEX execution_jobs_queued_idx ON execution_jobs (created_at)
    WHERE status = 'queued';
-- Reaper path: find jobs whose heartbeat went stale.
CREATE INDEX execution_jobs_status_heartbeat_idx ON execution_jobs (status, last_heartbeat_at);
CREATE INDEX execution_jobs_execution_idx ON execution_jobs (execution_id);

-- ----------------------------------------------------------- ws_tickets ----
-- Short-lived single-use tokens for WebSocket handshakes, which cannot carry an
-- Authorization header and must not put a JWT in the URL.
CREATE TABLE ws_tickets (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash bytea NOT NULL,
    scope      varchar(120) NOT NULL,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT ws_tickets_token_hash_key UNIQUE (token_hash)
);

CREATE INDEX ws_tickets_expires_at_idx ON ws_tickets (expires_at);



