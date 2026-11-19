-- 密码最短长度限制已移除：创建账号、改密与注册都不再强制最短位数。
-- 系统设置里不再需要该策略，直接删除列，避免留下不生效的配置项。
ALTER TABLE system_settings DROP COLUMN password_min_length;
