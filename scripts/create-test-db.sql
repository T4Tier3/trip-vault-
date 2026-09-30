-- Creates the database the integration tests use.
--
-- It is a separate database from the development one on purpose. The test
-- suite drops and recreates the public schema, so sharing a database meant
-- `go test ./...` could destroy the schema of a running `make run-api`.
--
-- No schema is applied here. The Go migrator owns the schema, and
-- testsupport refuses to drop a schema in a database whose name does not
-- contain "test", which is what keeps this file and that check in agreement.
--
-- Note for the initdb lifecycle: this only runs when the data volume is first
-- created. After changing this file, `docker compose down -v` to re-run it.

SELECT 'CREATE DATABASE tripvault_test'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'tripvault_test')\gexec

-- A scratch database for the test that proves the destructive-path guard holds.
--
-- The name deliberately does NOT contain "test", because the guard refuses to
-- drop a schema in any database whose name does. That refusal is the behaviour
-- under test, so the database has to be one the guard rejects.
--
-- It is safe for the guard test to point here: if the guard ever regressed, the
-- worst outcome is that this empty scratch database is dropped, not anyone's
-- development data. Pointing that test at the real development database would
-- risk exactly that.
SELECT 'CREATE DATABASE tripvault_guard'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'tripvault_guard')\gexec
