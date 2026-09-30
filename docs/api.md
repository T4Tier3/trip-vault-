# API reference

Base URL `http://localhost:8080`. All endpoints are under `/api/v1` except the
health probes, which are unversioned on purpose so an orchestrator can probe a
fixed path.

## Conventions

**Success** is the payload under `data`:

```json
{ "data": { "id": "…", "email": "sam@example.com" } }
```

**Failure** is a stable machine code plus a sentence written for a human:

```json
{
  "error": {
    "code": "validation_failed",
    "message": "Some fields need attention.",
    "fields": { "email": "Enter a valid email address." },
    "requestId": "01JQ8Z4H7K2M9V3B"
  }
}
```

The codes are for branching; the messages are for showing. Clients should not
parse messages, and the app maps codes to its own copy where it has any.

| Code | Status | Meaning |
| --- | --- | --- |
| `validation_failed` | 400 | A field failed its rules. See `fields`. |
| `invalid_request` | 400 | Malformed body or unsupported method. |
| `unauthorized` | 401 | Missing, malformed or unparseable access token. |
| `token_expired` | 401 | Access token expired. Refresh and retry. |
| `token_invalid` | 400 or 401 | Emailed token wrong/used/expired (400), or the session cannot be refreshed (401). |
| `forbidden` | 403 | Authenticated, but not allowed. |
| `not_found` | 404 | No such endpoint or resource. |
| `conflict` | 409 | The request contradicts current state. |
| `email_taken` | 409 | That address already has an account. |
| `weak_password` | 400 | Password passed length but failed the strength policy. `fields.password` explains. |
| `rate_limited` | 429 | Too many requests, on a credential endpoint in particular. `Retry-After` says when to retry. |
| `email_not_verified` | 401 | The account must confirm its address first. |
| `invalid_credentials` | 401 | Wrong email or password. Never which one. |
| `request_too_large` | 413 | Body over the limit. |
| `internal_error` | 500 | Something broke. Quote the `requestId`. |

Every response carries an `X-Request-ID` header, and the same value appears as
`requestId` in an error body. A `429` also carries `Retry-After` plus
`X-RateLimit-Limit`, `X-RateLimit-Remaining` and `X-RateLimit-Reset`.

Two validation codes share the 400 status: `validation_failed` for a field that
breaks a shape rule, and `weak_password` for a password that is long enough but
not varied enough. They are separate codes because they mean different things to
the person typing, and both carry a `fields` entry. Branch on the code, not the
status.

`weak_password` is only ever returned where a password is being *set* —
registration and password reset. At sign-in a weak password is reported as
`invalid_credentials` like any wrong password, because "that password is too
weak" would confirm that the account exists and that the password matched
something.

## Authentication

`/api/v1/auth` carries a tighter rate limit than the rest of the API.

### POST /api/v1/auth/register

```json
{ "email": "sam@example.com", "password": "correct horse battery", "name": "Sam" }
```

`201`:

```json
{
  "data": {
    "userId": "…",
    "email": "sam@example.com",
    "name": "Sam",
    "needsVerification": true,
    "message": "Account created. Check your email to verify your address."
  }
}
```

**Does not sign in.** The account still has to confirm its address, and
returning tokens here would hand out a session for an unverified account.
`needsVerification` is what the client should branch on, rather than assuming it
from the fact that the user just registered.

Password rules: **10 characters minimum**, at most 256, and at least two of
lower case, upper case, digits, or symbols. A password under the minimum is
`validation_failed` with a `fields.password` entry; one that clears the minimum
but fails the character mix is `weak_password`, also with a `fields.password`
entry. Both are 400, but the code tells the form which message to show.

Two is deliberately the threshold, not three. Requiring three rejects a long
passphrase built entirely from lower case, which is a perfectly strong password,
and pushes people toward predictable substitutions like `Passw0rd!`. A short list
of breached passwords is also refused, but that check is server-side only: the
form does not ship a partial copy of the list, and the rejection arrives as
`weak_password` with a `fields.password` entry.

`409 email_taken` if the address is in use. This is the one endpoint that
confirms an address exists, and it has to: a person typing an address they
already used needs to know, and a form that accepts the address would be worse.

### POST /api/v1/auth/login

```json
{ "email": "sam@example.com", "password": "…" }
```

`200`:

```json
{
  "data": {
    "user": { "id": "…", "email": "…", "name": "…", "emailVerified": true, "createdAt": "…" },
    "tokens": {
      "accessToken": "…",
      "refreshToken": "…",
      "tokenType": "Bearer",
      "expiresAt": "2026-09-29T16:56:06Z",
      "expiresIn": 899
    }
  }
}
```

`401 invalid_credentials` for a wrong email, a wrong password, or an address
that does not exist — the response and the timing do not distinguish them.
`403 email_not_verified` if the address is unconfirmed.

### POST /api/v1/auth/refresh

`{ "refreshToken": "…" }` → the same shape as login, with a new pair.

Presenting an already-rotated token revokes the entire token family and returns
`401 token_invalid`. That is deliberate: see
[architecture.md](architecture.md#token-design).

Every refresh failure returns the same code and the same message — unknown,
expired, revoked, or replayed — so the endpoint cannot be used to find out
whether a token was ever real. The message is *"Your session has ended. Please
sign in again."* Note it is not the sign-in failure: a refresh request carries
neither an email nor a password, and telling someone their password is wrong
sends them to the wrong screen.

### POST /api/v1/auth/logout

`{ "refreshToken": "…" }` → `200`. Idempotent, and never reports whether the
token was valid.

### POST /api/v1/auth/verify-email

`{ "token": "…" }` → `200`. The token is single use; a second attempt is
`400 token_invalid`. **Does not sign in.**

The token may also be given as a `?token=` query parameter, for a link opened in
a browser or by anything that cannot send a body. It is not what makes the deep
link work: the link itself carries the token, the app parses it out of
`tripvault://verify-email?token=…`, and posts it in the JSON body. A query
string on the request would end up in access and proxy logs, and a token should
not.

### POST /api/v1/auth/resend-verification

`{ "email": "…" }` → `200`, always, whether or not the account exists.

### POST /api/v1/auth/forgot-password

`{ "email": "…" }` → `200`, always. If the address exists, mail goes out with a
`tripvault://reset-password?token=…` link.

These two return `200` rather than `204` because they always carry a body with a
message for the user to read.

### POST /api/v1/auth/reset-password

`{ "token": "…", "password": "…" }` → `200`.

Accepts the new password only if it passes the same policy as registration, and
revokes every session in the same transaction. A password reset that leaves old
sessions alive does not actually end a compromise.

### Lifetimes

Worth stating in one place, because a client showing "this link expires in N
minutes" has to get it from here rather than by guessing.

| Credential | Lifetime | Set by |
| --- | --- | --- |
| Access token | 15 minutes | `JWT_ACCESS_EXPIRATION` |
| Refresh token | 30 days | `JWT_REFRESH_EXPIRATION` |
| Email verification token | 24 hours | `auth.EmailVerificationTTL` |
| Password reset token | 1 hour | `auth.PasswordResetTTL` |
| Reuse grace period | 10 seconds | `JWT_REFRESH_GRACE_PERIOD` |

A refresh token is rotated on every use. The grace period is the window in which
the previously issued token is still accepted, so a request that races a refresh
does not sign the user out; it is short on purpose, because it is exactly the
window in which a stolen token is still useful.

## Users

Requires `Authorization: Bearer <access token>`.

### GET /api/v1/users/me

The authenticated user:

```json
{
  "data": {
    "id": "…", "email": "…", "name": "…",
    "emailVerified": true, "createdAt": "…", "updatedAt": "…"
  }
}
```

No password hash, and not by omission — the response type has no field for one.

### PATCH /api/v1/users/me

```json
{ "name": "Sam Rivera" }
```

`200` with the user. Only `name` is changeable; email changes need a
re-verification flow, which is a later phase.

## Health

### GET /health

Liveness. `200` if the process is running. It does not touch the database, so a
database outage does not get the API killed and restarted in a loop.

### GET /ready

Readiness. `200` only if PostgreSQL and Redis both answer, otherwise `503`:

```json
{
  "status": "degraded",
  "dependencies": {
    "postgres": { "status": "ok", "latencyMs": 1 },
    "redis": { "status": "unavailable", "latencyMs": 0 }
  }
}
```

The probes are the one exception to the `data` envelope: an orchestrator
reading them should not have to unwrap anything, and a bare status object is
easier to eyeball when a probe is failing.

Use this one for load balancer membership, and `/health` for restart decisions.
