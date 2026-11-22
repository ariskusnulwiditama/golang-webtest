-- 000001_init.down — reverses 000001_init.up.sql.
--
-- Development/test convenience only; dropping the schema destroys all data.
-- Drop order is the reverse of creation so foreign keys never block a drop.

DROP TABLE IF EXISTS ws_tickets;
DROP TABLE IF EXISTS execution_jobs;
DROP TABLE IF EXISTS test_screenshots;
DROP TABLE IF EXISTS execution_logs;
DROP TABLE IF EXISTS execution_results;
DROP TABLE IF EXISTS test_executions;
DROP TABLE IF EXISTS environments;
DROP TABLE IF EXISTS test_steps;
DROP TABLE IF EXISTS test_cases;
DROP TABLE IF EXISTS test_suites;
DROP TABLE IF EXISTS projects;
DROP TABLE IF EXISTS users;

-- citext is left installed: other databases on the same cluster may rely on it.
