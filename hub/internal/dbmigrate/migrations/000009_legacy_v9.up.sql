-- 迁移 v9：服务器直连地址
-- 本文件由旧 migrate_sql.go 自动导出，内容与之逐字一致。
-- 禁止修改：改内容会让老库与新库结构不一致，且难以被发现。

ALTER TABLE agents ADD COLUMN direct_address TEXT NOT NULL DEFAULT '';
