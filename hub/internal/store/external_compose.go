package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
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
	var out ExternalComposeDraft
	err := d.db.QueryRowContext(ctx, `SELECT content,source_hash,version,updated_at FROM external_compose_drafts WHERE agent_id=? AND name=?`, agent, name).Scan(&out.Content, &out.SourceHash, &out.Version, &out.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	return out, err
}
func (d *DB) SaveExternalComposeDraft(ctx context.Context, agent, name, content, hash string, expected int64) (ExternalComposeDraft, error) {
	out := ExternalComposeDraft{Content: content, SourceHash: hash, Version: expected + 1, UpdatedAt: time.Now().UTC().Unix()}
	if expected == 0 {
		_, err := d.db.ExecContext(ctx, `INSERT INTO external_compose_drafts(agent_id,name,content,source_hash,version,updated_at) VALUES(?,?,?,?,?,?)`, agent, name, content, hash, out.Version, out.UpdatedAt)
		return out, normalizeExecError(err, "保存外部编排草稿")
	}
	res, err := d.db.ExecContext(ctx, `UPDATE external_compose_drafts SET content=?,source_hash=?,version=version+1,updated_at=? WHERE agent_id=? AND name=? AND version=?`, content, hash, out.UpdatedAt, agent, name, expected)
	if err != nil {
		return out, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return out, err
	}
	if n != 1 {
		return out, ErrConflict
	}
	return out, nil
}
