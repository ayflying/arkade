package store

import (
	"arkade/hub/internal/audit"
	"context"
	"fmt"
	"testing"
	"time"
)

func TestAuditCountRetention(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	// 批量灌入旧库状态，包含同一秒的记录，避免逐条写测试拖慢回归。
	err := d.Tx(ctx, func(tx *sqlTx) error {
		for i := 0; i < AuditMaxRecords+5; i++ {
			_, err := tx.ExecContext(ctx, `INSERT INTO audit_logs(id,user_id,username,agent_id,agent_name,action,target,detail,result,error,duration_ms,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, fmt.Sprintf("id-%06d", i), "", "", "", "", "test", "", "", "success", "", 0, now.Unix())
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.PruneAuditCount(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	_ = d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs`).Scan(&count)
	if count != AuditMaxRecords {
		t.Fatalf("count %d", count)
	}
	var min string
	_ = d.db.QueryRowContext(ctx, `SELECT MIN(id) FROM audit_logs`).Scan(&min)
	if min != "id-000005" {
		t.Fatal("wrong oldest record", min)
	}
	if err := d.WriteAudit(audit.Record{ID: "latest", CreatedAt: now.Add(time.Second), Action: "test"}); err != nil {
		t.Fatal(err)
	}
	_ = d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs`).Scan(&count)
	if count != AuditMaxRecords {
		t.Fatal("write exceeded limit", count)
	}
	_ = d.db.QueryRowContext(ctx, `SELECT MIN(id) FROM audit_logs WHERE id LIKE 'id-%'`).Scan(&min)
	if min != "id-000006" {
		t.Fatal("write removed wrong record", min)
	}
}
