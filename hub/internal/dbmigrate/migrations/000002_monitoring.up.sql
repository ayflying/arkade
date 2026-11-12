-- Monitoring state is independent of agents: deleting an agent must retain history.
CREATE TABLE monitor_config (
    id integer PRIMARY KEY CHECK (id = 1),
    config_json text NOT NULL DEFAULT '{}',
    secrets_ciphertext text NOT NULL DEFAULT '',
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0)
);
INSERT INTO monitor_config (id) VALUES (1);

CREATE TABLE monitor_incidents (
    id text PRIMARY KEY,
    fingerprint text NOT NULL UNIQUE,
    agent_id text NOT NULL DEFAULT '',
    container_id text NOT NULL DEFAULT '',
    kind text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT '',
    first_seen bigint NOT NULL DEFAULT 0,
    last_seen bigint NOT NULL DEFAULT 0,
    last_notified bigint NOT NULL DEFAULT 0,
    resolved_at bigint NOT NULL DEFAULT 0,
    detail_json text NOT NULL DEFAULT '{}'
);
CREATE INDEX idx_monitor_incidents_history ON monitor_incidents (last_seen DESC, id DESC);
CREATE INDEX idx_monitor_incidents_open ON monitor_incidents (status, last_seen DESC);

CREATE TABLE monitor_deliveries (
    id text PRIMARY KEY,
    incident_id text NOT NULL DEFAULT '',
    channel text NOT NULL DEFAULT '',
    event text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT '',
    error text NOT NULL DEFAULT '',
    created_at bigint NOT NULL DEFAULT 0
);
CREATE INDEX idx_monitor_deliveries_history ON monitor_deliveries (created_at DESC, id DESC);
