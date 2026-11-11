-- 迁移 v7：编排项目保存环境文件
-- 本文件由旧 migrate_sql.go 自动导出，内容与之逐字一致。
-- 禁止修改：改内容会让老库与新库结构不一致，且难以被发现。

ALTER TABLE compose_stacks ADD COLUMN env_content TEXT NOT NULL DEFAULT '';
