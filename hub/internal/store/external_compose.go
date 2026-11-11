package store

import (
	"context"
	"time"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/errors/gerror"

	"arkade/hub/internal/dao"
	"arkade/hub/internal/model/do"
)

// ExternalComposeDraft与已纳管记录分开，编辑草稿不应让自动发现误判为已接管。
// source_hash记录编辑基线；version用于浏览器间CAS，不写宿主文件。
type ExternalComposeDraft struct {
	Content    string `json:"content"`
	SourceHash string `json:"source_hash"`
	Version    int64  `json:"version"`
	UpdatedAt  int64  `json:"updated_at"`
}

func (d *DB) GetExternalComposeDraft(ctx context.Context, agent, name string) (ExternalComposeDraft, error) {
	c := dao.ExternalComposeDrafts.Columns()
	one, err := d.gdb.Model(dao.ExternalComposeDrafts.Table()).Ctx(ctx).
		Fields(c.Content, c.SourceHash, c.Version, c.UpdatedAt).
		Where(c.AgentId, agent).
		Where(c.Name, name).
		One()
	if err != nil {
		return ExternalComposeDraft{}, gerror.Wrap(err, "查询外部编排草稿失败")
	}
	if one.IsEmpty() {
		return ExternalComposeDraft{}, ErrNotFound
	}

	var out ExternalComposeDraft
	out.Content = one[c.Content].String()
	out.SourceHash = one[c.SourceHash].String()
	out.Version = one[c.Version].Int64()
	out.UpdatedAt = one[c.UpdatedAt].Int64()
	return out, nil
}

// SaveExternalComposeDraft 保存草稿，expected 是调用方看到的版本号（乐观锁）。
//
// 并发安全来自 **WHERE version=? 与 RowsAffected 检查**：SET 与 WHERE 在同一条
// UPDATE 里原子匹配，匹配不到就影响 0 行，据此返回 ErrConflict。
// `version + 1` 用 gdb.Raw 表达只是相对自增的常见写法，不是安全性的必要条件——
// 在 Go 里算好 expected+1 再写入，正确性同样成立（对应关系仍是 expected→expected+1）。
func (d *DB) SaveExternalComposeDraft(ctx context.Context, agent, name, content, hash string, expected int64) (ExternalComposeDraft, error) {
	out := ExternalComposeDraft{
		Content:    content,
		SourceHash: hash,
		Version:    expected + 1,
		UpdatedAt:  time.Now().UTC().Unix(),
	}
	c := dao.ExternalComposeDrafts.Columns()

	if expected == 0 {
		_, err := d.gdb.Model(dao.ExternalComposeDrafts.Table()).Ctx(ctx).Data(do.ExternalComposeDrafts{
			AgentId:    agent,
			Name:       name,
			Content:    content,
			SourceHash: hash,
			Version:    out.Version,
			UpdatedAt:  out.UpdatedAt,
		}).Insert()
		return out, normalizeExecError(err, "保存外部编排草稿")
	}

	res, err := d.gdb.Model(dao.ExternalComposeDrafts.Table()).Ctx(ctx).
		Where(c.AgentId, agent).
		Where(c.Name, name).
		Where(c.Version, expected).
		Data(do.ExternalComposeDrafts{
			Content:    content,
			SourceHash: hash,
			Version:    gdb.Raw("version + 1"),
			UpdatedAt:  out.UpdatedAt,
		}).
		Update()
	if err != nil {
		return out, gerror.Wrap(err, "保存外部编排草稿失败")
	}

	n, err := res.RowsAffected()
	if err != nil {
		return out, gerror.Wrap(err, "保存外部编排草稿失败（读取影响行数）")
	}
	if n != 1 {
		// 0 行 = 版本不匹配（别人先改了）；>1 行 = 数据异常（agent+name 应唯一）。
		// 两种情况都不能当作成功：前者是并发冲突，后者说明约束缺失。
		return out, ErrConflict
	}
	return out, nil
}
