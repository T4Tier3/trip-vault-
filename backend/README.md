# TripVault API

The Go service. Authentication and profiles today; trips and media will be
added to this same binary.

For the API surface see [../docs/api.md](../docs/api.md), and for how it is put
together see [../docs/architecture.md](../docs/architecture.md).

## Running it

From the repository root, so the shared `.env` is picked up:

```bash
make env      # once: create .env with generated JWT secrets
make up       # PostgreSQL, Redis, Mailpit
make migrate
make run-api
```

Or directly:

```bash
export $(grep -v '^#' ../.env | xargs)
go run ./cmd/server
```

Requires Go 1.26. `sqlc` is not used; repositories are hand-written pgx with
`sqlc`-style structure, so there is no code generation step.

## Commands

```bash
make test        # unit + integration
make test-race   # with the race detector
make lint        # gofmt check + go vet + golangci-lint if installed
make cover       # coverage.out, and the total
make build       # bin/tripvault-api
```

Tests need PostgreSQL and Redis. They use the `docker compose` services and
**skip** rather than fail when those are absent, so `go test ./...` is safe to
run anywhere. A green run with skips is not a green run; CI starts the services
to avoid that. When checking locally, treat a skip count above zero as a failure:

```bash
go test ./... -count=1 -v 2>&1 | grep -c '^--- SKIP'   # want 0
```

### Test databases

`go test ./...` runs each package's test binary as a separate process, in
parallel. They do not share a database: `testsupport` redirects each test binary
to its own `tripvault_test_<package>` database, creating it on first use. Sharing
one database was not safe, because a package testing the migrator's repair path
drops the public schema, which pulls it out from under whatever other package
happened to be migrating at the time. The symptom is a `pg_proc_proname_args_nsp_index`
duplicate key error in a package that did nothing wrong.

To start clean:

```bash
docker compose down -v && docker compose up -d   # re-runs the init scripts
```

Two guards worth knowing about:

- The integration tests only ever touch a database whose name contains `test`.
  `testsupport.DropSchema` refuses to run against anything else, so `go test`
  cannot drop a real schema even if `DATABASE_URL` points somewhere it should
  not.
- The development database (`tripvault`) is separate from every test database.
  `make test` and `make run-api` can run at the same time.

Set `TEST_DATABASE_URL` to point the tests at a different server entirely.
`TESTDEV_DATABASE_URL` is only used by the guard test and may safely name a
non-test database, since nothing writes to it.

## Layout

```
cmd/server/          entry point, wiring, graceful shutdown, token pruning
internal/config/     typed config, validated at startup
internal/database/   pool, transactions, migrator, embedded migrations
internal/httpx/      response envelope, errors, validation, context helpers
internal/middleware/ request id, recovery, CORS, security headers, rate limits
internal/auth/       service, handlers, token repository, issuer
internal/user/       profile service and repository
internal/emailserver/ SMTP mailer and notifier
internal/storage/    Redis rate limiter
internal/testsupport/ database fixtures
```

Handlers hold no decisions. The service holds the rules and knows nothing about
HTTP. Repositories hold SQL and no rules. The auth tests drive the real router,
so if that layering slips the tests notice.

## Migrations

```bash
make migrate-up
make migrate-status
make migrate-down     # rolls back one
make migrate-reset    # DESTRUCTIVE, prompts first
```

Also available as `go run ./cmd/server -migrate-only=up|down|status|reset`,
which is what the container image uses as its entrypoint override in a deploy
pipeline.

## Container

```bash
docker build -t tripvault-backend:dev -f Dockerfile .
docker run --rm -p 8080:8080 --env-file ../.env \
  -e DATABASE_URL='postgresql://postgres:postgres@host.docker.internal:5432/tripvault?sslmode=disable' \
  -e REDIS_URL='redis://host.docker.internal:6379/0' \
  tripvault-backend:dev
```

The two overrides are not optional on a laptop. `.env` points at `localhost`,
which inside a container is the container itself, so without them the server
starts and then exits on `ping postgres: connection refused`. On Linux use
`--add-host=host.docker.internal:host-gateway` if the name does not resolve.

Distroless, non-root (`nonroot:nonroot`), static binary. Configuration comes
from the environment; there is no config file in the image. The image has no
shell, so `docker run ... sh` will not work — use `-migrate-only` instead:

```bash
docker run --rm --env-file ../.env \
  -e DATABASE_URL='postgresql://postgres:postgres@host.docker.internal:5432/tripvault?sslmode=disable' \
  tripvault-backend:dev -migrate-only=status
```

## Adding an endpoint

1. Request struct with a `ValidationFields` method, in the handler, next to the
   handler.
2. Error codes in `internal/httpx/response.go`. Add a new one rather than reusing
   `internal_error` — the client branches on these.
3. Service method. No `gin.Context` in the signature.
4. Repository method. No business rules; if a method needs to decide something,
   that decision belongs in the service.
5. Handler method, registered on the group for its authentication level.
6. A test in `internal/server/auth_flow_test.go` that goes through the router.

## Things that will bite you

- **Never log a token.** Not the value, not a prefix of it, not on an error
  path. A mail send failure is exactly when someone is tempted to print the
  body.
- **Never return an `error`'s text to a client.** Use a constructor from
  `httpx`; the cause is logged, the message is for the user.
- **Do not verify a user id that came from a body or a header.** It comes from
  `httpx.CurrentUser(c)`, which is only populated by `RequireAuth`.
- **Do not delete rotated refresh tokens.** Reuse detection needs them. Pruning
  is time-based and lives in `main.go`.
- **Keep a token out of the query string.** The API accepts `?token=` on
  `verify-email` for cold-start deep links, but a query string lands in access
  logs. Use the JSON body.
- **Password reset revokes all sessions in the same transaction** as the
  password change. Separating them leaves a window where a compromised session
  survives a reset.
- **The pool runs in `QueryExecModeDescribeExec` on purpose.** Switching to
  pgx's faster default reintroduces `SQLSTATE 40574` on every rolling deploy,
  where a migration recreates objects under a live connection. There is a
  regression test in `internal/database/postgres_test.go`; read it before
  changing the mode.
