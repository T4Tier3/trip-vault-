# TripVault

A private photo and video vault for group trips. People upload to a shared
trip, everyone else sees thumbnails, and the full-resolution files stay private
to whoever uploaded them.

This repository is at the end of phase 2: the foundation and a complete,
tested authentication vertical slice. Trips, uploads and sharing are not built
yet.

## Layout

```
backend/     Go API (Gin, pgx, Redis)
mobile/      React Native client
docs/        Architecture, API reference, database notes
```

## Getting started

You need Docker and Go 1.26 or newer.

```bash
make env        # create .env with freshly generated JWT secrets
make up         # start PostgreSQL, Redis and Mailpit
make migrate    # apply the schema
make run-api    # serve on http://localhost:8080
```

Mailpit's web UI is at <http://localhost:8025>. Emails the API sends, including
verification and password reset links, land there instead of going out.

To check it is alive:

```bash
curl -s localhost:8080/health
curl -s localhost:8080/ready    # also reports on Postgres and Redis
```

`make help` lists everything.

## Where things stand

| Area | State |
| --- | --- |
| Monorepo, Docker, config, migrations | done |
| Registration, sign in, sign out | done |
| Email verification, password reset | done |
| Rotating refresh tokens with reuse detection | done |
| Profile read and update | done |
| Rate limiting, security headers, CORS | done |
| Mobile sign in, register, verify | done |
| Trips, albums, uploads, sharing | not started |

The mobile client is TypeScript only. The native `ios/` and `android/` projects
are generated rather than committed; see `mobile/README.md`.

## Checks

```bash
make check          # backend vet + tests, mobile typecheck + tests
make test           # backend only
make test-mobile    # mobile only
```

Backend tests run against real PostgreSQL and Redis through Docker Compose, not
mocks: the auth tests exercise the actual router, middleware, repositories and
SQL. Each test binary gets its own `tripvault_test_<package>` database, so
`make test` and `make run-api` can run at the same time without interfering.

Tests skip themselves rather than fail when the services are missing, because
`go test ./...` should be safe to run anywhere. A green run with skips is not a
green run, so treat a non-zero skip count as a failure:

```bash
cd backend && go test ./... -count=1 -v 2>&1 | grep -c '^--- SKIP'   # want 0
```

CI enforces that rather than trusting a human to remember.

## Documentation

- [docs/architecture.md](docs/architecture.md) — how the pieces fit, and why
- [docs/api.md](docs/api.md) — every endpoint, with request and response shapes
- [docs/database.md](docs/database.md) — schema and the reasoning behind it
- [backend/README.md](backend/README.md) — running and extending the API
- [mobile/README.md](mobile/README.md) — running the app, and the deep link setup

## Design decisions worth knowing

**Refresh tokens rotate, and reuse is treated as theft.** Each refresh mints a
new token and revokes the old one. Presenting an already-used token means either
a replay or a stolen token, so the whole family is revoked and the user is
forced to sign in. The alternative, leaving tokens valid until expiry, means a
leaked token stays useful for weeks.

**Verification and reset tokens are stored hashed**, like passwords, so a
database dump does not hand over working credentials.

**Password reset revokes every session** in the same transaction that changes
the password, so a stolen session does not outlive the credential it was issued
against.

**Email links open the app, not a web page.** The API serves no HTML, so an
`https` link in a password reset email would be a dead end, and a dead reset
link is a permanent lockout. Links use the app's URL scheme instead. That is why
`APP_LINK_SCHEME` is validated at startup: a malformed scheme is caught before it
reaches anyone's inbox.

**Nothing sensitive is logged.** Responses carry a request id that also appears
in the logs, so a user reporting a problem can be matched to a log line without
transcribing anything secret.

**Images are not proxied through this service.** TripVault stores no media in
PostgreSQL. Originals go to private object storage; this API hands out signed
URLs. Keeping large files out of the database is what lets the auth slice stay
fast and the backup story stay simple.

**The database pool does not trust a cached plan.** pgx's default
`QueryExecModeCacheStatement` assumes the schema never changes under a live
connection. It does, on every rolling deploy, where a migration recreates tables
while the previous version is still serving; PostgreSQL then rejects the query
with `SQLSTATE 40574` and the endpoint answers 500 against a perfectly healthy
database. The pool uses `QueryExecModeDescribeExec` instead, which re-describes
a stale statement rather than failing. It costs one extra round trip per query.
There is a regression test, because this is the kind of fix that gets reverted as
"unnecessary overhead".
