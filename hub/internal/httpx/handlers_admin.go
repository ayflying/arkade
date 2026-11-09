package httpx

// handlers_admin.go 放「管理类接口」共用的东西：admin 闸门、JSON 局部更新字段、
// 请求体解析、仓储错误到 HTTP 错误的统一映射。
//
// 为什么单独成文件：用户与用户组两组接口有大量相同的参数处理与错误映射，
// 复制到两个 handler 文件里必然出现「用户接口改了错误码、组接口忘了改」的分裂。

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
)

// requireAdmin 是管理类接口的统一闸门：只允许 admin 角色。
//
// 为什么用户与组管理必须是 admin 而不是「manage 档权限」：manage 是
// 「对某些服务器的最高权限」（决策 D-04 的组×服务器授权），而用户与组本身是
// 权限的来源。允许普通用户改用户组，等于让他自己给自己发权限，整个权限模型会被绕过。
//
// 返回 false 表示已写过响应，调用方应立即 return。
func requireAdmin(w http.ResponseWriter, d Deps, r *http.Request) (user.User, bool) {
	u, ok := userFrom(r.Context())
	if !ok {
		// 理论上不可达（均已过认证中间件），防御性处理，与认证中间件保持一致的 401。
		respondError(w, d.Log, envelope.CodeUnauthorized, "登录已失效，请重新登录")
		return user.User{}, false
	}
	if u.Role != user.RoleAdmin {
		respondError(w, d.Log, envelope.CodeForbidden, "该操作仅管理员可用")
		return user.User{}, false
	}
	return u, true
}

// respondStoreError 把仓储层错误映射成统一的 HTTP 错误响应。
//
// 映射口径（全项目唯一，不要在 handler 里各写一份）：
//   - ErrNotFound → 404 not_found（消息用调用方给的业务名词）
//   - ErrConflict → 409 conflict（重名）
//   - 其余         → 500 internal（记录日志，不把 SQL 细节透给前端）
func respondStoreError(w http.ResponseWriter, d Deps, err error, notFoundMsg string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		respondError(w, d.Log, envelope.CodeNotFound, notFoundMsg)
	case errors.Is(err, store.ErrConflict):
		respondError(w, d.Log, codeConflict, "名称已被占用，请换一个")
	default:
		d.Log.Error("管理接口数据库操作失败", "err", err)
		respondError(w, d.Log, envelope.CodeInternal, "数据库操作失败")
	}
}

// codeConflict 是「唯一约束冲突」的错误码。
//
// protocol 包中没有这个常量（那是 Agent↔Hub 的信封协议码，不承担 Hub 的
// 业务语义），因此在本包内定义一次并保持稳定：前端按 409 + code=conflict
// 展示「名称已被占用」。respond.go 的 statusForCode 已同步识别它。
const codeConflict = "conflict"

// optional 表达「JSON 里的字段是否出现」。
//
// 为什么不能用普通指针（*string）直接解码：encoding/json 里 null 与「字段缺失」
// 都会让指针变成 nil，无法区分「显式清空」与「没提到这个字段」。
// PATCH 语义要求区分这两者，因此用 Set 标记是否在 JSON 中出现过。
type optional[T any] struct {
	Value T
	Set   bool
}

// UnmarshalJSON 记录字段是否出现并解析值。
//
// 显式传 null 时视为「出现且为零值」：对 PATCH 来说 null 表示「清空为默认值」，
// 比静默忽略更符合直觉；而真正不允许修改的字段（如用户名）根本不会进入结构体。
func (o *optional[T]) UnmarshalJSON(data []byte) error {
	o.Set = true
	if string(data) == "null" {
		var zero T
		o.Value = zero
		return nil
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	o.Value = v
	return nil
}

// ptrIfSet 把 optional 转成仓储层要的指针语义（nil 表示本次不修改）。
func ptrIfSet[T any](o optional[T]) *T {
	if !o.Set {
		return nil
	}
	v := o.Value
	return &v
}

// decodeAndValidate 解析请求体并统一处理「格式错误」的 400 响应。
// 返回 false 表示已写过响应。
func decodeAndValidate(w http.ResponseWriter, d Deps, r *http.Request, out any) bool {
	if err := decodeJSON(r, out); err != nil {
		respondError(w, d.Log, envelope.CodeBadRequest, "请求格式错误: "+err.Error())
		return false
	}
	return true
}

// badRequest 是参数校验失败的统一出口。
func badRequest(w http.ResponseWriter, d Deps, format string, args ...any) {
	respondError(w, d.Log, envelope.CodeBadRequest, fmt.Sprintf(format, args...))
}
