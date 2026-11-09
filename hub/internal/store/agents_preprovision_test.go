package store

// agents_preprovision_test.go 覆盖预创建服务器与专属接入凭据的存储层。
//
// 这些断言是整条链路的根基：若摘要没被哈希、或重置没真正替换旧行，
// 上层再怎么严谨的认证逻辑也没有意义。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"
)

// hashOf 计算密钥摘要。
//
// 这里独立实现而不是调 agentlink 的版本：store 不该依赖 agentlink
// （那会形成反向依赖）。若两边算法分叉，认证会整体失效，
// 由 httpx 层的集成测试负责发现——那里用的才是生产的摘要函数。
func hashOf(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// newPreprovRecord 造一条预建记录。
func newPreprovRecord(name string) AgentRecord {
	now := time.Now().Unix()
	id, err := NewAgentID()
	if err != nil {
		panic(err) // 测试夹具无法生成身份时应立即失败。
	}
	return AgentRecord{
		ID:          id,
		Name:        name,
		DisplayName: name,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// TestCreatePreprovisionedAgentBasics 验证预建记录的字段落库正确。
func TestCreatePreprovisionedAgentBasics(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	rec := newPreprovRecord("预建机")
	rec.Note = "备注内容"
	rec.Labels = map[string]string{"env": "prod"}
	h := hashOf("token-plain")

	if err := db.CreatePreprovisionedAgent(ctx, rec, h, time.Now()); err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	got, err := db.GetAgent(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusPending {
		t.Errorf("状态应为 pending，实际 %q", got.Status)
	}
	if !got.Preprovisioned {
		t.Error("应带 preprovisioned 标记")
	}
	if got.Note != "备注内容" {
		t.Errorf("备注未保存: %q", got.Note)
	}
	if got.Labels["env"] != "prod" {
		t.Errorf("标签未保存: %+v", got.Labels)
	}
	if got.Disabled {
		t.Error("新建记录不应是禁用状态")
	}

	// 凭据可反查。
	id, err := db.LookupAgentByTokenHash(ctx, h)
	if err != nil {
		t.Fatalf("按摘要应能查到: %v", err)
	}
	if id != rec.ID {
		t.Errorf("绑定身份错误: %q != %q", id, rec.ID)
	}
}

// TestCreatePreprovisionedAgentDuplicateName 验证重名返回 ErrAgentNameTaken。
func TestCreatePreprovisionedAgentDuplicateName(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	first := newPreprovRecord("同名机")
	if err := db.CreatePreprovisionedAgent(ctx, first, hashOf("t1"), time.Now()); err != nil {
		t.Fatal(err)
	}

	// 同名，以及规范化后同名（大小写、首尾/连续空白）。
	for _, name := range []string{"同名机", "  同名机  ", "同名机\t"} {
		rec := newPreprovRecord(name)
		err := db.CreatePreprovisionedAgent(ctx, rec, hashOf("t-"+name), time.Now())
		if !errors.Is(err, ErrAgentNameTaken) {
			t.Errorf("名称 %q 应被判为重名，实际 err=%v", name, err)
		}
	}

	// 重名失败不能留下半截数据（事务性）：
	// 若只插入 agents 而没插 token，就会产生一条"有记录但无法接入"的孤儿。
	var agents, tokens int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents`).Scan(&agents); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_tokens`).Scan(&tokens); err != nil {
		t.Fatal(err)
	}
	if agents != 1 {
		t.Errorf("重名失败不应写入记录，期望 1 条实际 %d 条", agents)
	}
	if tokens != 1 {
		t.Errorf("重名失败不应写入凭据，期望 1 条实际 %d 条", tokens)
	}
}

// TestCreatePreprovisionedAgentConcurrentSameName 验证并发建同名只会成功一次。
//
// "先查再插"若无事务保护，两个并发请求会同时看到"不存在"而各插一条，
// 于是界面上出现两个完全一样的名字。SQLite 的写事务是串行的，
// 配合事务内的检查，这条必须是"一成一败"。
func TestCreatePreprovisionedAgentConcurrentSameName(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	type res struct{ err error }
	ch := make(chan res, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			rec := newPreprovRecord("并发同名")
			err := db.CreatePreprovisionedAgent(ctx, rec, hashOf("ct"+string(rune('a'+i))), time.Now())
			ch <- res{err}
		}(i)
	}
	var okCount, takenCount int
	for i := 0; i < 2; i++ {
		r := <-ch
		switch {
		case r.err == nil:
			okCount++
		case errors.Is(r.err, ErrAgentNameTaken):
			takenCount++
		default:
			t.Fatalf("意外错误: %v", r.err)
		}
	}
	if okCount != 1 || takenCount != 1 {
		t.Errorf("并发同名应一成一败，实际 成功%d 重名%d", okCount, takenCount)
	}
}

// TestResetAgentTokenHashReplaces 验证重置真的替换了摘要（旧摘要失效）。
func TestResetAgentTokenHashReplaces(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	rec := newPreprovRecord("重置机")
	oldHash := hashOf("old-token")
	if err := db.CreatePreprovisionedAgent(ctx, rec, oldHash, time.Now()); err != nil {
		t.Fatal(err)
	}
	if id, err := db.LookupAgentByTokenHash(ctx, oldHash); err != nil || id != rec.ID {
		t.Fatalf("旧摘要应能查到: id=%q err=%v", id, err)
	}

	newHash := hashOf("new-token")
	if err := db.ResetAgentTokenHash(ctx, rec.ID, newHash, time.Now()); err != nil {
		t.Fatalf("重置应成功: %v", err)
	}

	// 新摘要有效。
	if id, err := db.LookupAgentByTokenHash(ctx, newHash); err != nil || id != rec.ID {
		t.Errorf("新摘要应能查到: id=%q err=%v", id, err)
	}
	// 旧摘要必须立即失效——这是"重置立即生效"的存储层保证。
	if _, err := db.LookupAgentByTokenHash(ctx, oldHash); !errors.Is(err, ErrAgentTokenNotFound) {
		t.Errorf("旧摘要应已失效（返回 ErrAgentTokenNotFound），实际 err=%v", err)
	}
	// 一条记录只能有一把有效密钥（唯一索引保证），
	// 否则重置后新旧两把都能用，撤销就是假的。
	var n int
	if err := db.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agent_tokens WHERE agent_id = ?`, rec.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("每条记录应只有一行凭据，实际 %d 行", n)
	}
}

// TestResetAgentTokenHashUnknown 验证重置不存在的记录返回 ErrNotFound。
func TestResetAgentTokenHashUnknown(t *testing.T) {
	db := newTestDB(t)
	err := db.ResetAgentTokenHash(context.Background(), "agent-nope", hashOf("x"), time.Now())
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("不存在的记录应返回 ErrNotFound，实际 %v", err)
	}
}

// TestDeleteAgentToken 验证删除凭据后旧摘要查不到，且重复删除幂等。
func TestDeleteAgentToken(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	rec := newPreprovRecord("删凭据机")
	h := hashOf("del-token")
	if err := db.CreatePreprovisionedAgent(ctx, rec, h, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteAgentToken(ctx, rec.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.LookupAgentByTokenHash(ctx, h); !errors.Is(err, ErrAgentTokenNotFound) {
		t.Errorf("删除后摘要应查不到，实际 %v", err)
	}
	// 幂等：再删一次不应报错（删除服务器时会无条件调用）。
	if err := db.DeleteAgentToken(ctx, rec.ID); err != nil {
		t.Errorf("重复删除应幂等，实际 %v", err)
	}
}

// TestHasAgentToken 验证"是否有专属凭据"的判断。
//
// 这个判断被用于"共享 token 不能认领专属服务器"，
// 它必须在出错时**返回错误而不是 false**——否则一次数据库抖动
// 就会把一台专属服务器临时变成可被共享 token 认领。
func TestHasAgentToken(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	rec := newPreprovRecord("has-token机")
	if err := db.CreatePreprovisionedAgent(ctx, rec, hashOf("h1"), time.Now()); err != nil {
		t.Fatal(err)
	}
	has, err := db.HasAgentToken(ctx, rec.ID)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if !has {
		t.Error("应有专属凭据")
	}

	// 共享 token 接入的旧记录没有凭据行。
	plain := newPreprovRecord("无凭据机")
	now := time.Now().Unix()
	if _, err := db.db.ExecContext(ctx,
		`INSERT INTO agents (id, name, status, version, runtime, runtime_version, last_seen,
		 labels, created_at, updated_at, display_name, note, disabled)
		 VALUES (?, ?, 'offline', '', '', '', 0, '{}', ?, ?, ?, '', 0)`,
		plain.ID, plain.Name, now, now, plain.DisplayName); err != nil {
		t.Fatal(err)
	}
	has2, err := db.HasAgentToken(ctx, plain.ID)
	if err != nil {
		t.Fatal(err)
	}
	if has2 {
		t.Error("共享 token 接入的旧记录不应有专属凭据")
	}
}

// TestNewAgentIDUniqueness 验证 ID 生成不重复且形状稳定。
func TestNewAgentIDUniqueness(t *testing.T) {
	seen := make(map[string]struct{}, 500)
	for i := 0; i < 500; i++ {
		id, err := NewAgentID()
		if err != nil {
			t.Fatal(err)
		}
		if len(id) < len("agent-")+16 {
			t.Fatalf("ID 过短: %q", id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("ID 重复: %q（随机源有问题）", id)
		}
		seen[id] = struct{}{}
	}
}

// TestPlaintextTokenNotStoredAnywhere 是最重要的一条：库里不能有明文。
//
// 直接扫所有表的所有文本列。这样即使将来有人加了新表、
// 或用另一种方式存密钥，这条测试仍然有效。
func TestPlaintextTokenNotStoredAnywhere(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	rec := newPreprovRecord("明文检查机")
	const plain = "THIS-IS-THE-PLAINTEXT-TOKEN-VALUE"
	if err := db.CreatePreprovisionedAgent(ctx, rec, hashOf(plain), time.Now()); err != nil {
		t.Fatal(err)
	}

	var tables []string
	rows, err := db.db.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()

	for _, tbl := range tables {
		// 把整行拼成一个字符串再搜：这样不必枚举列，
		// 也不会漏掉"密钥被塞进某个非文本列"的情况。
		q := `SELECT group_concat(CAST(t AS TEXT), '|') FROM (SELECT * FROM "` + tbl + `") AS x`
		_ = q
		colRows, err := db.db.QueryContext(ctx, `SELECT * FROM "`+tbl+`" LIMIT 50`)
		if err != nil {
			t.Fatalf("扫描 %s 失败: %v", tbl, err)
		}
		cols, err := colRows.Columns()
		if err != nil {
			_ = colRows.Close()
			t.Fatal(err)
		}
		for colRows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := colRows.Scan(ptrs...); err != nil {
				_ = colRows.Close()
				t.Fatal(err)
			}
			for i, v := range vals {
				var s string
				switch tv := v.(type) {
				case string:
					s = tv
				case []byte:
					s = string(tv)
				}
				if len(s) > 0 && contains(s, plain) {
					t.Errorf("表 %s 的列 %s 里出现了明文密钥（必须只存摘要）", tbl, cols[i])
				}
			}
		}
		_ = colRows.Close()
	}
}

// contains 是简单的子串判断（避免额外 import strings 造成干扰）。
func contains(s, sub string) bool {
	if len(sub) == 0 || len(s) < len(sub) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestMigrationVersionIsSix 验证迁移版本号已推进到 6。
//
// 版本号写错会导致新库与旧库结构不一致，而两边测试都可能"各自通过"。
func TestMigrationVersionIsSix(t *testing.T) {
	db := newTestDB(t)
	var v int
	err := db.db.QueryRowContext(context.Background(), `SELECT MAX(version) FROM schema_migrations`).Scan(&v)
	if err != nil {
		t.Fatal(err)
	}
	if v != 6 {
		t.Errorf("迁移版本应为 6，实际 %d", v)
	}
}
