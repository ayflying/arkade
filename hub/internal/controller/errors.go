package controller

// errors.go 定义控制器返回值里的**业务错误**。
//
// ## 为什么不用 gerror.NewCode 那一套
//
// GoFrame 原生示例用 `gerror.NewCode(gcode.CodeMissingParameter, …)`
// 配合 MiddlewareHandlerResponse 输出 `{"code":…,"message":…}`。
// Hub 的错误契约是 `{"error":{"code":"…","message":"…"}}`，
// 形状不同，且我们不启用那层中间件。
//
// 所以错误用本项目自己的类型表达：适配层（bindGF）按类型里的
// HTTP 状态与 code 写成标准错误信封。这样控制器不直接操作
// ResponseWriter，也不产生第二套错误序列化逻辑。
//
// ## 安全约定
//
// Message 会**原样出现在响应体**里，因此只能放给用户看的话：
// 不能含 SQL 片段、表名、内部路径、账号是否存在等信息。
// 需要详情的场景一律记日志，响应里给模糊文案。

import "fmt"

// Error 是控制器可以返回的业务错误。
type Error struct {
	// Status 是 HTTP 状态码。
	Status int
	// Code 是响应体里的 error.code（前端据此分支）。
	Code string
	// Message 是给用户看的文案。
	Message string
	// err 是可选的底层原因，只进日志、不进响应。
	err error
}

func (e *Error) Error() string {
	if e.err != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.err)
	}
	return e.Message
}

// Unwrap 支持 errors.Is/As 穿透到底层原因。
func (e *Error) Unwrap() error { return e.err }

// Err 返回底层原因（供适配层记日志用；它不会进响应体）。
func (e *Error) Err() error { return e.err }

// WithErr 返回带底层原因的错误副本（原因只进日志）。
func (e *Error) WithErr(err error) *Error {
	cp := *e
	cp.err = err
	return &cp
}

// Is 让同 Status+Code 的两个错误可以通过 errors.Is 匹配，
// 便于控制器内部用哨兵错误做分支。
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Status == e.Status && t.Code == e.Code
}
