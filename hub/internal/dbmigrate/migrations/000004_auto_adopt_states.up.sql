-- 外部编排自动发现（auto adopt）的**扫描状态**。
--
-- 为什么必须落库：状态原先只存在 Hub 进程内存里，重启即清空，
-- 于是每次重启都会把所有"读不到配置"的外部项目重新扫一遍，
-- 每个项目每轮都创建一个 helper 容器。配合"失败项永不停止重扫"，
-- 这一组合正是容器列表堆积几十个 arkade-helper-* 的根因。
--
-- 语义：
--   - status: skipped（确定性阻塞，不再自动重扫） / failed（环境性失败，退避重试）
--   - attempts: 环境性失败的累计次数，用于指数退避与次数上限
--   - next_check_at: 下次可以再扫的时间（unix 秒），确定性阻塞写 0
--
-- 不记录成功接管的项目：成功由 compose_stacks.synced_at 表达，
-- 两处各存一份只会让"谁才算已接管"出现两个答案。
CREATE TABLE auto_adopt_states (
 agent_id TEXT NOT NULL, name TEXT NOT NULL,
 status TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '',
 attempts BIGINT NOT NULL DEFAULT 1, next_check_at BIGINT NOT NULL DEFAULT 0,
 last_scanned_at BIGINT NOT NULL DEFAULT 0,
 PRIMARY KEY(agent_id,name)
);
CREATE INDEX idx_auto_adopt_states_next ON auto_adopt_states(agent_id,next_check_at);
