package controller

// errors_builtin.go 定义控制器通用错误。
//
// 这些是"任何接口都可能返回"的状态语义，集中定义避免各处
// 手写 Status/Code 造成不一致。

import "net/http"

var (
	// ErrBadRequest 请求格式/参数不合法。
	ErrBadRequest = &Error{Status: http.StatusBadRequest, Code: "bad_request", Message: "请求参数不正确"}

	// ErrUnauthorized 未登录或凭据无效。
	ErrUnauthorized = &Error{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "登录已失效"}

	// ErrForbidden 已登录但无权限。
	ErrForbidden = &Error{Status: http.StatusForbidden, Code: "forbidden", Message: "没有权限执行该操作"}

	// ErrNotFound 目标不存在。
	ErrNotFound = &Error{Status: http.StatusNotFound, Code: "not_found", Message: "资源不存在"}

	// ErrConflict 唯一约束冲突或状态不允许。
	ErrConflict = &Error{Status: http.StatusConflict, Code: "conflict", Message: "操作冲突"}

	// ErrUnavailable 依赖不可用（数据库、Agent）。
	ErrUnavailable = &Error{Status: http.StatusServiceUnavailable, Code: "agent_offline", Message: "依赖服务不可用"}

	// ErrInternal 内部错误。Message 会被替换为兜底文案（见 WriteError）。
	ErrInternal = &Error{Status: http.StatusInternalServerError, Code: "internal", Message: "服务器内部错误"}
)
