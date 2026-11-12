-- System policy and explicit OIDC subject bindings. Existing local users retain
-- their credentials and do not acquire a forced password change on upgrade.
ALTER TABLE users ADD COLUMN must_change_password INTEGER NOT NULL DEFAULT 0;

CREATE TABLE system_settings (
 id INTEGER PRIMARY KEY CHECK (id = 1),
 site_name TEXT NOT NULL,
 login_notice TEXT NOT NULL DEFAULT '',
 registration_enabled INTEGER NOT NULL DEFAULT 0 CHECK (registration_enabled IN (0,1)),
 registration_group_id TEXT REFERENCES groups(id) ON DELETE SET NULL,
 session_ttl_seconds INTEGER NOT NULL,
 password_min_length INTEGER NOT NULL,
 login_max_attempts INTEGER NOT NULL,
 login_window_seconds INTEGER NOT NULL,
 audit_retention_days INTEGER NOT NULL,
 version INTEGER NOT NULL DEFAULT 1
);
INSERT INTO system_settings(id,site_name,session_ttl_seconds,password_min_length,login_max_attempts,login_window_seconds,audit_retention_days)
 VALUES(1,'Arkade',86400,12,5,900,90);

CREATE TABLE oidc_providers (
 id TEXT PRIMARY KEY,
 display_name TEXT NOT NULL,
 issuer TEXT NOT NULL,
 client_id TEXT NOT NULL,
 client_secret_cipher TEXT NOT NULL DEFAULT '',
 scopes TEXT NOT NULL DEFAULT 'openid profile email',
 enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0,1)),
 auto_register INTEGER NOT NULL DEFAULT 0 CHECK (auto_register IN (0,1)),
 default_group_id TEXT REFERENCES groups(id) ON DELETE SET NULL,
 version INTEGER NOT NULL DEFAULT 1,
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
);
CREATE TABLE user_identities (
 id TEXT PRIMARY KEY,
 user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 provider_id TEXT NOT NULL REFERENCES oidc_providers(id) ON DELETE CASCADE,
 subject TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 UNIQUE(provider_id,subject),
 UNIQUE(user_id,provider_id)
);
CREATE INDEX idx_user_identities_user ON user_identities(user_id);
