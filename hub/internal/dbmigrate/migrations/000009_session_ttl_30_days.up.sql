-- 历史数据库种子是 24 小时（86400 秒），不是启动配置的 72 小时。
-- version=1 表示未通过系统设置保存过；不能区分保存过的 24 小时是默认值
-- 还是管理员明确选定的时长，因此保守地保留所有已保存过的设置。
-- 递增版本可使升级前读到的设置版本失效；现有 sessions.expires_at 不变。
UPDATE system_settings
   SET session_ttl_seconds = 2592000,
       version = version + 1
 WHERE id = 1 AND session_ttl_seconds = 86400 AND version = 1;
