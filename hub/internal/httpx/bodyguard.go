package httpx

// bodyguard.go 让 GF Bind 路径只认 JSON body，不被 query/form 影响。
//
// ## 问题（源码确认）
//
// GF 的 `Bind` 最终走 `doGetRequestStruct` → `r.GetRequestMap()`
// （ghttp_request_param_request.go:62），它按顺序合并：
//
//	r.parseQuery() + r.parseForm() + r.parseBody()
//
// 也就是说 query 与 form 都会参与结构体绑定：JSON 里的 username
// 可能被 `?username=` 影响，`Content-Type: form` 也能冒充 JSON 请求。
// 旧 decodeJSON 的语义是明确的：**不看 CT、只读 body、只按 JSON 解**。
//
// ## 两个互斥的职责，不能混在一起
//
//  1. **严格解码**（需要目标类型）：拒绝未知字段、拒绝非法 JSON。
//     只有 JSON body 接口（登录）需要；无体接口（/healthz）不需要。
//  2. **来源净化**（不需要目标类型）：剥掉 query 与 form 类 CT。
//     所有走 GF Bind 的路由都要，与是否有 body 无关。
//
// 早期实现把两者都挂在 `target != nil` 下，导致无体接口既不清 query、
// 又因为空 body 被误判；也会在有 target 但空 body 时提前 return 而不清 query
// （`POST /login?username=..&password=..` 空 body 仍可凭 query 登录）。
//
// 因此这里拆成两步，净化**永远执行**，解码**只在需要且有 body 时**执行。

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// MaxBodyBytes 是请求体上限（与迁移前一致的 1 MiB）。
const MaxBodyBytes = 1 << 20

// errBodyTooLarge 表示请求体超限。
var errBodyTooLarge = errors.New("请求体过大")

// sanitizeBody 净化参数来源，并在需要时严格校验 JSON body。
//
// target 为 nil 表示本接口不做 body 解码（例如 /healthz），
// 此时仍会执行来源净化，且**完全不触碰** body 内容。
//
// 返回的请求必须被后续流程使用。
func sanitizeBody(req *http.Request, target any) (*http.Request, error) {
	// ① 来源净化：与是否有 body 无关，必须先做。
	//    不清 query 的话，空 body + `?username=x&password=y` 能直接登录。
	stripQuery(req)
	stripFormContentType(req)

	// ② 不需要解码的接口到此为止：保留原 body 原样，不读不丢。
	if target == nil {
		return req, nil
	}

	// ③ 严格解码 JSON body。
	if req.Body == nil || req.Body == http.NoBody {
		// 无 body：不解析，也不报错。
		// 空字段的判定交给控制器（它会给 400 和准确文案）。
		return req, nil
	}

	raw, err := io.ReadAll(io.LimitReader(req.Body, MaxBodyBytes+1))
	_ = req.Body.Close()
	// 无论成败都还原 body：后续（审计、hook、GF 的 Parse）仍需要读它。
	req.Body = io.NopCloser(bytes.NewReader(raw))

	if err != nil {
		return req, errors.New("请求体读取失败")
	}
	if int64(len(raw)) > MaxBodyBytes {
		return req, errBodyTooLarge
	}
	// 空 body 不解析：交给控制器判空字段（与旧行为一致）。
	if len(bytes.TrimSpace(raw)) == 0 {
		return req, nil
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return req, errors.New("请求格式错误")
	}
	return req, nil
}

// stripQuery 剥掉 URL 的 query 部分。
//
// GF 的 parseQuery 从 URL.RawQuery 取值，清空它即可阻断 query 参与绑定。
// 这些接口的语义全部来自 body，query 从未被支持。
func stripQuery(req *http.Request) {
	if req.URL == nil || req.URL.RawQuery == "" {
		return
	}
	u := *req.URL
	u.RawQuery = ""
	u.ForceQuery = false
	req.URL = &u
	// RequestURI 也一并清理：GF 部分路径会解析它。
	req.RequestURI = u.RequestURI()
}

// stripFormContentType 去掉表单类 Content-Type。
//
// GF 的 parseForm 按 CT 分派；不清理的话，声明为 form 的请求会走表单路径。
// 注意**不**改写为 application/json：旧实现不看 CT，
// 这里只是阻止 form 分派，保持"只认 body"的等价语义。
func stripFormContentType(req *http.Request) {
	ct := req.Header.Get("Content-Type")
	if !isFormContentType(ct) {
		return
	}
	req.Header.Del("Content-Type")
}

// isFormContentType 判断是否为表单类 CT。
func isFormContentType(ct string) bool {
	const form = "application/x-www-form-urlencoded"
	const multipart = "multipart/form-data"
	return hasPrefix(ct, form) || hasPrefix(ct, multipart)
}

// hasPrefix 是 strings.HasPrefix 的局部等价物（避免为此引入依赖）。
func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
