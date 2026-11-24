-- 审计保留期收敛到 30 天上限。
--
-- 为什么既要这份迁移，又要在 store 打开库时再收敛一次：
--   - 迁移负责**已经在跑的实例**：它们那一行可能是 90（旧默认）或
--     3650（旧上限内被管理员调大过），升级后必须一次性收到 30；
--   - 打开库时的那次 UPDATE 负责**未来**：有人手工改库、或从别处
--     导入了一份超限配置，重启即恢复合规（见 audit_retention_floor.go）。
--
-- 只改超出 [1,30] 的值：管理员主动设置的 7 天这类合法值必须保留。
UPDATE system_settings
   SET audit_retention_days = 30
 WHERE audit_retention_days > 30 OR audit_retention_days < 1;
