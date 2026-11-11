-- 迁移 v4：Agent 表补记显示名、备注与禁用状态
-- 本文件由旧 migrate_sql.go 自动导出，内容与之逐字一致。
-- 禁止修改：改内容会让老库与新库结构不一致，且难以被发现。

ALTER TABLE agents ADD COLUMN display_name TEXT NOT NULL DEFAULT '';

ALTER TABLE agents ADD COLUMN note TEXT NOT NULL DEFAULT '';

ALTER TABLE agents ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0;
