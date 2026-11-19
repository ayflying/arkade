-- Administrator-owned MCP credentials. Only the SHA-256 digest is persisted;
-- the public ID is independent of the random secret and safe to use in audit logs.
CREATE TABLE admin_api_keys (
 id TEXT PRIMARY KEY,
 user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 name TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80),
 token_hash TEXT NOT NULL UNIQUE CHECK (token_hash ~ '^[0-9a-f]{64}$'),
 created_at BIGINT NOT NULL
);
CREATE INDEX idx_admin_api_keys_user ON admin_api_keys(user_id, created_at DESC, id);

-- A subsequent re-enable or promotion must not resurrect old credentials.
CREATE FUNCTION revoke_ineligible_admin_api_keys() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
 DELETE FROM admin_api_keys WHERE user_id = NEW.id;
 RETURN NEW;
END;
$$;
CREATE TRIGGER revoke_ineligible_admin_api_keys
 AFTER UPDATE OF disabled, role ON users FOR EACH ROW
 WHEN ((OLD.disabled, OLD.role) IS DISTINCT FROM (NEW.disabled, NEW.role)
       AND (NEW.disabled <> 0 OR NEW.role <> 'admin'))
 EXECUTE FUNCTION revoke_ineligible_admin_api_keys();
