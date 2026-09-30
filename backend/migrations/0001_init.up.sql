-- ---------------------------------------------------------------------------
-- 0001_init: authentication, users and devices.
--
-- Design notes
--   * Emails are stored already normalised to lower case, and a CHECK enforces
--     that invariant. A plain unique index is therefore enough for lookups and
--     avoids depending on the citext extension.
--   * Binary media is deliberately absent. Media lives in ImageKit; this table
--     only ever holds metadata and CDN references. See docs/database.md.
--   * Every token the API hands out (email verification, password reset,
--     refresh) is stored as a SHA-256 hash, never in plaintext. A database leak
--     must not hand an attacker usable credentials.
-- ---------------------------------------------------------------------------

-- Keeps updated_at honest without trusting application code to remember it.
CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- --------------------------------------------------------------------------
-- users
-- --------------------------------------------------------------------------
CREATE TABLE users (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    email          TEXT        NOT NULL,
    password_hash  TEXT        NOT NULL,
    name           TEXT        NOT NULL,
    avatar_url     TEXT,
    email_verified BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at     TIMESTAMPTZ,

    CONSTRAINT users_email_lowercase CHECK (email = lower(email)),
    CONSTRAINT users_email_not_blank CHECK (length(btrim(email)) > 3),
    CONSTRAINT users_name_not_blank  CHECK (length(btrim(name)) > 0)
);

-- Partial unique index: a soft-deleted account releases its email for reuse.
CREATE UNIQUE INDEX users_email_unique ON users (email) WHERE deleted_at IS NULL;

CREATE TRIGGER users_set_updated_at
    BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- --------------------------------------------------------------------------
-- auth_tokens: single-use, expiring tokens for email verification and
-- password reset. Purpose-separated so one token can never be replayed as the
-- other kind of credential.
-- --------------------------------------------------------------------------
CREATE TYPE auth_token_purpose AS ENUM ('email_verification', 'password_reset');

CREATE TABLE auth_tokens (
    id          UUID                PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID                NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    purpose     auth_token_purpose  NOT NULL,
    token_hash  TEXT                NOT NULL,
    expires_at  TIMESTAMPTZ         NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ         NOT NULL DEFAULT now(),

    CONSTRAINT auth_tokens_hash_unique UNIQUE (token_hash),
    CONSTRAINT auth_tokens_expiry_future CHECK (expires_at > created_at)
);

-- Serves "find my live tokens for this purpose" without scanning spent rows.
CREATE INDEX auth_tokens_user_purpose_live
    ON auth_tokens (user_id, purpose)
    WHERE consumed_at IS NULL;

-- --------------------------------------------------------------------------
-- refresh_tokens: rotating refresh tokens with family tracking.
--
-- family_id groups every token descended from one login. Presenting a token
-- that has already been rotated signals a stolen token, and the entire family
-- is revoked.
-- --------------------------------------------------------------------------
CREATE TABLE refresh_tokens (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id        UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    family_id      UUID        NOT NULL,
    token_hash     TEXT        NOT NULL,
    device_id      UUID,
    expires_at     TIMESTAMPTZ NOT NULL,
    rotated_at     TIMESTAMPTZ,
    revoked_at     TIMESTAMPTZ,
    revoked_reason TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT refresh_tokens_hash_unique UNIQUE (token_hash),
    CONSTRAINT refresh_tokens_expiry_future CHECK (expires_at > created_at)
);

-- Reuse detection and "revoke every session for this device" both walk a family.
CREATE INDEX refresh_tokens_family_idx ON refresh_tokens (family_id);
CREATE INDEX refresh_tokens_user_live_idx
    ON refresh_tokens (user_id)
    WHERE revoked_at IS NULL;

-- --------------------------------------------------------------------------
-- devices: push notification targets, registered on login and refreshed by the
-- mobile client. FCM/APNs delivery is built on top of this in Phase 8.
-- --------------------------------------------------------------------------
CREATE TABLE devices (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    platform         TEXT        NOT NULL,
    device_identifier TEXT,
    push_token       TEXT,
    app_version      TEXT,
    last_seen_at     TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT devices_platform_valid CHECK (platform IN ('ios', 'android', 'web')),
    -- One physical install registers once; re-login updates the same row.
    CONSTRAINT devices_user_identifier_unique UNIQUE (user_id, platform, device_identifier)
);

CREATE INDEX devices_user_idx ON devices (user_id);

CREATE TRIGGER devices_set_updated_at
    BEFORE UPDATE ON devices
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
