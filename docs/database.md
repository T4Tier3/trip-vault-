# Database notes

PostgreSQL 17. Four tables, all in the default schema, no extensions beyond
`pgcrypto`'s `gen_random_uuid()`.

Migrations are embedded in the binary (`internal/database/migrations`) and
applied in filename order inside a transaction, with the applied state tracked
in `schema_migrations`. Each migration is paired: `0001_init.up.sql` and
`0001_init.down.sql`.

## What is not here

**No image, video, or thumbnail columns.** Those are Phase 3+ and they belong in
object storage, with this database holding only metadata and CDN references.
Storing large binaries in PostgreSQL would make the database the slowest,
hardest-to-back-up and easiest-to-misconfigure part of the system, for no gain:
nothing queries photos with SQL.

**No `citext`.** Emails are stored lower-cased and a `CHECK` enforces it. The
invariant is in the database rather than in a collation setting, so it survives
a restore into a different environment.

## users

| Column | Type | Notes |
| --- | --- | --- |
| `id` | `UUID` | PK, `gen_random_uuid()` |
| `email` | `TEXT` | Lower-cased, `CHECK (email = lower(email))` |
| `password_hash` | `TEXT` | Argon2id, PHC string |
| `name` | `TEXT` | |
| `avatar_url` | `TEXT` | Nullable |
| `email_verified` | `BOOLEAN` | Default `false` |
| `created_at`, `updated_at` | `TIMESTAMPTZ` | `updated_at` by trigger |
| `deleted_at` | `TIMESTAMPTZ` | Soft delete |

Two details that are easy to get wrong later:

The unique index on email is **partial**: `WHERE deleted_at IS NULL`. A plain
unique constraint would stop a deleted account's address ever being used again,
and an address being permanently unusable is a support incident with no
workaround.

`email_verified` is a boolean rather than a timestamp. A date is not used yet,
and adding a nullable `email_verified_at` when resend-verification needs to
report "last sent" is cheaper than migrating a used column.

## auth_tokens

Single-use tokens for email verification and password reset.

| Column | Type | Notes |
| --- | --- | --- |
| `id` | `UUID` | PK |
| `user_id` | `UUID` | FK to `users`, `ON DELETE CASCADE` |
| `purpose` | `auth_token_purpose` | `'email_verification'` or `'password_reset'` |
| `token_hash` | `TEXT` | SHA-256. Unique. |
| `expires_at` | `TIMESTAMPTZ` | `CHECK (expires_at > created_at)` |
| `consumed_at` | `TIMESTAMPTZ` | Null until used |
| `created_at` | `TIMESTAMPTZ` | |

`purpose` is an enum, not a free text column, and that is the whole point: it
makes it impossible for a password reset token to be presented as a verification
token, which is the kind of mistake that a string column happily allows until
someone exploits it.

Tokens are stored as SHA-256 hashes. The tokens are 256 bits of random data, so
unlike a password they are not brute-forceable, and hashing them means a leaked
backup contains nothing usable. The cost is that a lost token cannot be
recovered — it is reissued, which is the correct outcome anyway.

The partial index `auth_tokens_user_purpose_live ON (user_id, purpose) WHERE
consumed_at IS NULL` serves the only query that runs in a hot path: "find this
user's live token for this purpose, so I can invalidate the old one when issuing
a new one."

## refresh_tokens

| Column | Type | Notes |
| --- | --- | --- |
| `id` | `UUID` | PK |
| `user_id` | `UUID` | FK to `users`, `ON DELETE CASCADE` |
| `family_id` | `UUID` | Every token from one sign-in |
| `token_hash` | `TEXT` | SHA-256. Unique. |
| `device_id` | `UUID` | Nullable; no FK yet |
| `expires_at` | `TIMESTAMPTZ` | |
| `rotated_at` | `TIMESTAMPTZ` | Set when superseded |
| `revoked_at` | `TIMESTAMPTZ` | Set when invalidated |
| `revoked_reason` | `TEXT` | `reused`, `logout`, `password_reset` |
| `created_at` | `TIMESTAMPTZ` | |

`family_id` has no foreign key and does not need one: it is a grouping label for
tokens that are already related through `user_id`, and making it a real
reference would add a second parent table for no benefit. It is indexed because
revoking a family is a `WHERE family_id = $1` over a table that grows with every
refresh.

Rows are not deleted on revoke. Keeping them is what makes reuse detection
possible: a rotated token is still there to be found, and only its *absence* or
`rotated_at` distinguishes it from a valid one. Expired rows are pruned on a
timer, well after the longest refresh lifetime, so nothing is deleted while it
could still be needed to detect a replay.

`device_id` is intentionally not yet a foreign key. The `devices` table exists
and the column is written, but the constraint is deferred until devices are
actually registered, because a hard FK on a column that is nullable and only
sometimes set is a source of insert failures with no current benefit.

## devices

Push notification targets: one row per physical install, keyed by
`(user_id, platform, device_identifier)`, so re-login updates rather than
duplicates. `push_token` is the FCM/APNs handle, populated in Phase 8.

## Migrations

```bash
make migrate                      # apply
cd backend
go run ./cmd/server -migrate-only=status
go run ./cmd/server -migrate-only=down      # roll back one
go run ./cmd/server -migrate-only=reset     # roll back everything, reapply
```

Two things that were wrong and are now tested:

`reset` rolled back only the last migration and then tried to reapply from the
top, which failed on the first attempt against a populated database. It now
rolls everything back before reapplying.

The down migration dropped the `auth_token_purpose` enum before the table that
uses it, and PostgreSQL refuses that. Tables are dropped first now.

## Rules for the next migration

- One direction per file, named `NNNN_name.up.sql` / `.down.sql`.
- The down migration must actually work against a populated database. Test it
  with `-migrate-only=reset`, not just on an empty one.
- No `IF NOT EXISTS` to paper over a mismatch. A migration that silently does
  nothing when the schema is not what it expected will hide a real problem.
- Never edit an applied migration. Add a new one, even for a typo.
