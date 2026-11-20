-- 容器资源历史样本：1 分钟桶，只保留最近 7 天。
--
-- 设计取舍：
--   * 一分钟一个桶，同分钟内多次采集覆盖为最后一次（last write wins）。采集
--     间隔可配（10–3600 秒），桶边界让"每个容器每分钟最多一行"成立，7 天
--     稳态约每容器 10080 行，不随采集间隔变密。
--   * 只保存白名单指标和已算好的速率，不保存 inspect、环境变量或标签原文。
--   * 与 agents 表没有外键：删除服务器或容器后历史在保留期内仍然可查。
--   * project 用于说明样本归属哪个已接管的编排项目；非空是写入前提之一。
CREATE TABLE monitor_samples (
    agent_id text NOT NULL,
    container_id text NOT NULL,
    project text NOT NULL DEFAULT '',
    container_name text NOT NULL DEFAULT '',
    bucket_at bigint NOT NULL,
    last_at bigint NOT NULL DEFAULT 0,
    state text NOT NULL DEFAULT '',
    stats_available boolean NOT NULL DEFAULT false,
    cpu_percent double precision NOT NULL DEFAULT 0,
    memory_bytes bigint NOT NULL DEFAULT 0,
    memory_limit_bytes bigint NOT NULL DEFAULT 0,
    memory_percent double precision NOT NULL DEFAULT 0,
    network_available boolean NOT NULL DEFAULT false,
    network_rx_per_second double precision NOT NULL DEFAULT 0,
    network_tx_per_second double precision NOT NULL DEFAULT 0,
    block_available boolean NOT NULL DEFAULT false,
    block_read_per_second double precision NOT NULL DEFAULT 0,
    block_write_per_second double precision NOT NULL DEFAULT 0,
    rates_available boolean NOT NULL DEFAULT false,
    snapshot_complete boolean NOT NULL DEFAULT false,
    PRIMARY KEY (agent_id, container_id, bucket_at)
);

-- 过期清理只按时间扫描，不需要读主键前缀。
CREATE INDEX idx_monitor_samples_bucket ON monitor_samples (bucket_at);
