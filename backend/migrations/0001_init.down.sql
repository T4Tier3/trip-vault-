-- Reverse of 0001_init. Drops in dependency order.
DROP TRIGGER IF EXISTS devices_set_updated_at ON devices;
DROP TRIGGER IF EXISTS users_set_updated_at ON users;

-- Tables first: auth_tokens.purpose uses auth_token_purpose, so the enum cannot
-- be dropped while that column still exists.
DROP TABLE IF EXISTS devices;
DROP TABLE IF EXISTS refresh_tokens;
DROP TABLE IF EXISTS auth_tokens;
DROP TABLE IF EXISTS users;

DROP TYPE IF EXISTS auth_token_purpose;

DROP FUNCTION IF EXISTS set_updated_at();
