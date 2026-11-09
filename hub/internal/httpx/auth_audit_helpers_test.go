package httpx

// auth_audit_helpers_test.go 提供登录相关测试的辅助函数。
//
// 单独成文件是因为它们被两组测试共用（登录审计、账号枚举防护），
// 塞进任一组都会让另一组产生意外的依赖。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkade/hub/internal/audit"
	"arkade/hub/internal/store"
)

// loginAttempt 发一次登录请求，成功返回 Cookie，失败返回 nil。
func (e *testEnv) loginAttempt(t *testing.T, username, password string) []*http.Cookie {
	t.Helper()
	rec, _ := e.do(t, http.MethodPost, "/api/auth/login",
		`{"username":"`+username+`","password":"`+password+`"}`, nil)
	if rec.Code != http.StatusOK {
		return nil
	}
	return rec.Result().Cookies()
}

// loginRaw 发一次登录请求，返回状态码与响应体原文。
//
// 用于比较"用户不存在"与"密码错误"两种失败是否**完全无法区分**。
func (e *testEnv) loginRaw(t *testing.T, username, password string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader(`{"username":"`+username+`","password":"`+password+`"}`))
	req.Header.Set("Content-Type", "application/json")
	e.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// storeAllAudit 返回"不加筛选、取足够多条"的查询参数。
func storeAllAudit() store.AuditFilters {
	return store.AuditFilters{Limit: store.AuditMaxLimit}
}

// auditLoginRecords 从审计表里筛出登录记录。
func auditLoginRecords(t *testing.T, e *testEnv) []audit.Record {
	t.Helper()
	recs, _, err := e.db.ListAuditLogs(t.Context(), store.AuditFilters{
		Action: audit.ActionUserLogin,
		Limit:  store.AuditMaxLimit,
	})
	if err != nil {
		t.Fatalf("查询登录审计失败: %v", err)
	}
	return recs
}
