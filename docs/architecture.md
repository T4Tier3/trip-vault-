# Architecture

## The shape of it

```
                    ┌──────────────┐
   iOS / Android ──▶│  Go API      │──▶ PostgreSQL   users, tokens, sessions
   (React Native)   │  :8080       │──▶ Redis        rate limits
                    │              │──▶ SMTP         verification, reset mail
                    └──────────────┘──▶ object store  media (Phase 3+, not this API)
```

One service, four dependencies. The auth slice is complete, and the trip and
media features will be added to this same API rather than beside it, because
they share the session, the authorisation rules, and the schema.

## Request path

A request goes through these layers in order, and each one is a place where a
future bug is easier to find because the layer is named:

1. **Recovery and request id** — a panic becomes a 500 with a request id, and
   every response carries that id.
2. **Security headers** — no sniffing, no framing, HSTS in production.
3. **CORS** — allowlist, not `*`. The list is empty by default, which suits a
   mobile client that is not subject to CORS at all.
4. **Rate limiting** — a global budget on `/api/v1`, and a tighter one on
   `/api/v1/auth`. Redis backed, so it holds across instances.
5. **Auth** — on the protected group, `RequireAuth` verifies the signature and
   expiry, then puts the user id in the request context. Handlers read it with
   `httpx.CurrentUser` and never from a body field or header they parse
   themselves.
6. **Binding and validation** — a request struct declares its own rules as a
   `ValidationFields` map, so the rules sit next to the fields they describe.
7. **Service** — the business rules, with no knowledge of HTTP.
8. **Repository** — SQL only, no business rules.
9. **Response** — handlers return data, `httpx.Respond` wraps it, and
   `httpx.Fail` turns an `APIError` into a response.

The last three matter most. A handler that makes a decision is a handler with
untested decisions; the auth tests in `internal/server/auth_flow_test.go` drive
the real router, so the layers are exercised together rather than mocked apart.

## Token design

This is the part of the system worth reading twice.

**Access tokens** are HS256 JWTs, short lived, stateless. The API cannot revoke
one before it expires, which is accepted because the window is small and the
alternative — a database read on every request — does not pay for itself here.

**Refresh tokens** are opaque 256-bit random strings, stored hashed. They are
long lived, and they rotate: every refresh mints a new token and marks the old
one rotated, inside one transaction.

**Families** make reuse detectable. All tokens descended from a single sign-in
share a `family_id`. When a refresh token that was already rotated is presented,
something is wrong with it — either a replay or a stolen token — and the API
cannot tell which. So it revokes the whole family and forces a fresh sign in.
A stolen token is then worth at most one refresh, and the legitimate user finds
out.

Two refreshes racing from two devices would look identical to reuse and would
sign the user out. The client prevents this: `ApiClient.refresh` is
single-flight, so a burst of parallel 401s produces one refresh call, not one
per request. Both halves are tested.

**Email verification and password reset tokens** are single use, expiring, and
purpose-separated by an enum column. Stored hashed, so a database dump contains
no working credential.

**Expired rows are pruned** on a timer after a grace period beyond the longest
token lifetime. Deleting at expiry instead would break reuse detection, which
depends on the rotated token still being there to compare against.

## Password handling

Argon2id with parameters from configuration, compared with `subtle`.ConstantTimeCompare.
Verification uses a dummy hash for unknown emails, so a missing account costs the
same work as a wrong password — otherwise response time answers the question
"is this address registered" that the response body is careful not to.

The rules are deliberately boring: at least 10 characters, and at least two of
the four character classes. Two, not three, so a long lower-case passphrase is
still accepted. Length is the property that matters, and a rule that pushes
people toward `Password1!` produces worse passwords than a longer passphrase.

## Enumeration

Sign in, forgot password, and resend verification all answer identically whether
or not the account exists. This is a deliberate trade: it costs a legitimate
user the feedback "that email is not registered", and it buys not turning the
API into an oracle for harvesting addresses.

The verification screen in the app follows the same rule. A message that
distinguished "no such account" from "sent" would undo the server's work.

## Email

The API sends mail and does not render it. There is no HTML template and no web
route, so links in mail point at the app's URL scheme
(`tripvault://reset-password?token=...`) rather than at a page. The scheme is
validated at startup; see `mobile/README.md` for the native registration that
has to match.

Sending mail is best effort. If SMTP is down, a registration still succeeds and
the failure is logged, because failing the request would leave the user with an
account they cannot use and no way to recover it.

## Observability

Structured logs, one line per request, no bodies and no credentials. The request
id is the join between a user's report and a log line. Verification and reset
tokens are never logged, in any form, including on error paths — the one place
that temptation shows up is when a mail send fails and someone wants to see what
was being sent.

## Testing

The backend tests run against real PostgreSQL and Redis, started by Docker
Compose. Mocking the database would test the mock; the queries are where the
subtle bugs live, and the migration and token-rotation tests are only worth
having because they use the real thing.

`internal/testsupport` prepares a database per test run and repairs the schema
if it finds it inconsistent, so a failed run does not poison the next one.

Coverage is reported across packages with `-coverpkg`, which is the honest number
for a codebase this shape: per-package coverage hides the fact that the same
error path is asserted from several angles.
