-- PostgreSQL initial schema. Released migrations are immutable; append forward-only versions.
CREATE TABLE users (
 id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL,
 password_hash TEXT NOT NULL, role TEXT NOT NULL,
 disabled INTEGER NOT NULL DEFAULT 0 CHECK (disabled IN (0,1)),
 created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL,
 must_change_password INTEGER NOT NULL DEFAULT 0 CHECK (must_change_password IN (0,1))
);
CREATE TABLE groups (
 id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, description TEXT NOT NULL DEFAULT '', created_at BIGINT NOT NULL
);
CREATE TABLE user_groups (
 user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 group_id TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE, PRIMARY KEY(user_id,group_id)
);
CREATE INDEX idx_user_groups_group ON user_groups(group_id);
CREATE TABLE group_agents (
 group_id TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
 agent_id TEXT NOT NULL, permission TEXT NOT NULL, PRIMARY KEY(group_id,agent_id)
);
CREATE INDEX idx_group_agents_agent ON group_agents(agent_id);
CREATE TABLE agents (
 id TEXT PRIMARY KEY, name TEXT NOT NULL, status TEXT NOT NULL, version TEXT NOT NULL DEFAULT '',
 runtime TEXT NOT NULL DEFAULT 'none', runtime_version TEXT NOT NULL DEFAULT '',
 last_seen BIGINT NOT NULL DEFAULT 0, labels TEXT NOT NULL DEFAULT '{}',
 created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL,
 display_name TEXT NOT NULL DEFAULT '', note TEXT NOT NULL DEFAULT '',
 disabled INTEGER NOT NULL DEFAULT 0 CHECK (disabled IN (0,1)),
 preprovisioned INTEGER NOT NULL DEFAULT 0 CHECK (preprovisioned IN (0,1)), direct_address TEXT NOT NULL DEFAULT ''
);
CREATE TABLE sessions (
 token_hash TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 created_at BIGINT NOT NULL, expires_at BIGINT NOT NULL
);
CREATE INDEX idx_sessions_user ON sessions(user_id);
CREATE INDEX idx_sessions_expires ON sessions(expires_at);
CREATE TABLE compose_stacks (
 id TEXT PRIMARY KEY, agent_id TEXT NOT NULL, name TEXT NOT NULL, content TEXT NOT NULL,
 config_hash TEXT NOT NULL, version BIGINT NOT NULL DEFAULT 1, created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL,
 synced_at BIGINT NOT NULL DEFAULT 0, env_content TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_compose_stacks_agent_name ON compose_stacks(agent_id,name);
CREATE INDEX idx_compose_stacks_agent ON compose_stacks(agent_id);
CREATE TABLE audit_logs (
 id TEXT PRIMARY KEY, user_id TEXT NOT NULL, username TEXT NOT NULL, agent_id TEXT NOT NULL DEFAULT '',
 agent_name TEXT NOT NULL DEFAULT '', action TEXT NOT NULL, target TEXT NOT NULL DEFAULT '',
 detail TEXT NOT NULL DEFAULT '', result TEXT NOT NULL, error TEXT NOT NULL DEFAULT '',
 duration_ms BIGINT NOT NULL DEFAULT 0, created_at BIGINT NOT NULL
);
CREATE INDEX idx_audit_created ON audit_logs(created_at DESC);
CREATE INDEX idx_audit_user ON audit_logs(user_id);
CREATE INDEX idx_audit_agent ON audit_logs(agent_id);
CREATE INDEX idx_audit_action ON audit_logs(action);
CREATE TABLE agent_tokens (token_hash TEXT PRIMARY KEY, agent_id TEXT NOT NULL, created_at BIGINT NOT NULL);
CREATE UNIQUE INDEX idx_agent_tokens_agent ON agent_tokens(agent_id);
CREATE TABLE external_compose_drafts (
 agent_id TEXT NOT NULL, name TEXT NOT NULL, content TEXT NOT NULL, source_hash TEXT NOT NULL,
 version BIGINT NOT NULL, updated_at BIGINT NOT NULL, PRIMARY KEY(agent_id,name)
);
CREATE TABLE compose_operation_requests (
 agent_id TEXT NOT NULL, request_id TEXT NOT NULL, name TEXT NOT NULL, action TEXT NOT NULL,
 fingerprint TEXT NOT NULL, task_id TEXT NOT NULL DEFAULT '', stack_id TEXT NOT NULL,
 stack_version BIGINT NOT NULL, config_hash TEXT NOT NULL, created_at BIGINT NOT NULL,
 finalized INTEGER NOT NULL DEFAULT 0 CHECK (finalized IN (0,1)), final_error TEXT NOT NULL DEFAULT '',
 record_removed INTEGER NOT NULL DEFAULT 0 CHECK (record_removed IN (0,1)), PRIMARY KEY(agent_id,request_id)
);
CREATE INDEX idx_compose_operations_task ON compose_operation_requests(agent_id,name,task_id);
CREATE INDEX idx_compose_operations_expiry ON compose_operation_requests(finalized,created_at);
CREATE TABLE system_settings (
 id INTEGER PRIMARY KEY CHECK (id=1), site_name TEXT NOT NULL, login_notice TEXT NOT NULL DEFAULT '',
 registration_enabled INTEGER NOT NULL DEFAULT 0 CHECK (registration_enabled IN (0,1)),
 registration_group_id TEXT REFERENCES groups(id) ON DELETE SET NULL,
 session_ttl_seconds BIGINT NOT NULL, password_min_length INTEGER NOT NULL, login_max_attempts INTEGER NOT NULL,
 login_window_seconds BIGINT NOT NULL, audit_retention_days INTEGER NOT NULL, version BIGINT NOT NULL DEFAULT 1
);
INSERT INTO system_settings(id,site_name,session_ttl_seconds,password_min_length,login_max_attempts,login_window_seconds,audit_retention_days)
 VALUES(1,'Arkade',86400,12,5,900,90);
CREATE TABLE oidc_providers (
 id TEXT PRIMARY KEY, display_name TEXT NOT NULL, issuer TEXT NOT NULL, client_id TEXT NOT NULL,
 client_secret_cipher TEXT NOT NULL DEFAULT '', scopes TEXT NOT NULL DEFAULT 'openid profile email',
 enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0,1)), auto_register INTEGER NOT NULL DEFAULT 0 CHECK (auto_register IN (0,1)),
 default_group_id TEXT REFERENCES groups(id) ON DELETE SET NULL, version BIGINT NOT NULL DEFAULT 1,
 created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL
);
CREATE INDEX idx_oidc_providers_group ON oidc_providers(default_group_id);
CREATE INDEX idx_settings_registration_group ON system_settings(registration_group_id);
CREATE TABLE user_identities (
 id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 provider_id TEXT NOT NULL REFERENCES oidc_providers(id) ON DELETE CASCADE, subject TEXT NOT NULL, created_at BIGINT NOT NULL,
 UNIQUE(provider_id,subject), UNIQUE(user_id,provider_id)
);
CREATE INDEX idx_user_identities_user ON user_identities(user_id);
CREATE TABLE arkade_migrate_checksums (
 version BIGINT PRIMARY KEY, checksum TEXT NOT NULL CHECK(length(checksum)=64),
 applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
