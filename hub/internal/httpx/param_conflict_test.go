package httpx

// param_conflict_test.go 验证请求参数来源冲突时的行为。
//
// ## 为什么这条必须测
//
// GoFrame 的 `r.Parse()` 会把 query、form、body 全部合并进同一张参数表，
// `Bind` 到 Req 结构体时并不区分来源。这意味着：
//
//	POST /api/auth/login?username=admin
//	{"username":"alice","password":"..."}
//
// controller 收到的 Username 可能是 "admin"（query 胜出）而不是
// body 里的 "alice"。
//
// 旧实现（decodeJSON 直接读 body）不存在这个问题——它只认 JSON。
// 迁移若引入来源混淆，最坏的后果是**登录身份可被 query 参数劫持**：
// 攻击者构造链接让受害者点击，body 里的密码配上攻击者指定的用户名。
//
// 所以这里不是"测个边界"，是守一条安全边界。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"arkade/hub/internal/auth"
)

// TestLoginQueryDoesNotOverrideBody 验证 query 不能覆盖 JSON body 的字段。
//
// 构造方式：query 放**有效**凭据，body 放**无效**用户名。
// 两种结果可区分：
//   - 用 body（正确）：用户名 nobody 不存在 → 401，且不下发 Cookie
//   - 用 query（错误）：admin + 正确密码 → 200，且下发会话 Cookie
func TestLoginQueryDoesNotOverrideBody(t *testing.T) {
	e := newTestEnv(t)

	body := map[string]string{"username": "nobody", "password": "TestPass123!"}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("编码请求体失败: %v", err)
	}

	q := url.Values{}
	q.Set("username", "admin")
	q.Set("password", "TestPass123!")
	target := e.wsURL + "/api/auth/login?" + q.Encode()

	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("query 参数不应覆盖 JSON body：期望 401（body 里的 nobody 登录失败），"+
			"实际 %d —— 说明 username/password 被 query 劫持，这是安全缺陷", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == auth.CookieName {
			t.Fatalf("登录失败却下发了会话 Cookie（%s），说明 query 凭据被采纳", c.Name)
		}
	}
}

// TestLoginEmptyBodyQueryCannotLogin 验证空 body 时 query 不能登录。
//
// 这是净化逻辑最容易漏的分支：若"有 target 但 body 为空"时提前返回，
// 就跳过了清 query 的步骤，于是
//
//	POST /api/auth/login?username=admin&password=TestPass123!   空 body
//
// 会凭 query 登录成功。净化必须与 body 有无无关。
func TestLoginEmptyBodyQueryCannotLogin(t *testing.T) {
	e := newTestEnv(t)

	q := url.Values{}
	q.Set("username", "admin")
	q.Set("password", "TestPass123!")
	target := e.wsURL + "/api/auth/login?" + q.Encode()

	// 完全空的 body，CT 声明 JSON。
	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusOK {
		t.Fatalf("空 body + query 凭据不应登录成功，实际 200 —— " +
			"说明空 body 分支跳过了清除 query")
	}
	for _, c := range resp.Cookies() {
		if c.Name == auth.CookieName {
			t.Fatalf("空 body + query 凭据下发了会话 Cookie（%s）", c.Name)
		}
	}
}

// TestLoginQueryStillParsesBody 验证净化 query 没有破坏正常 JSON 登录。
func TestLoginQueryStillParsesBody(t *testing.T) {
	e := newTestEnv(t)

	body := map[string]string{"username": "admin", "password": "TestPass123!"}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("编码请求体失败: %v", err)
	}

	// 带上无关 query，不应影响正常登录。
	req, err := http.NewRequest(http.MethodPost,
		e.wsURL+"/api/auth/login?foo=bar", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("带无关 query 的正常登录应 200，实际 %d", resp.StatusCode)
	}
}

//
// 场景：Content-Type 是 application/json（走严格 JSON 路径），
// 但实际发的是 form 编码内容。此时必须按 400 拒绝，
// 而不是让 GF 把它当参数表解析后"成功登录"。
func TestLoginFormDoesNotOverrideBody(t *testing.T) {
	e := newTestEnv(t)

	form := "username=admin&password=TestPass123%21"
	req, err := http.NewRequest(http.MethodPost, e.wsURL+"/api/auth/login",
		strings.NewReader(form))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	// 声明 JSON 却发 form：属于格式错误，应 400。
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusOK {
		t.Fatalf("Content-Type 声明 JSON 但内容是 form 时应拒绝，实际 200 —— "+
			"说明请求体没被按 JSON 严格解析")
	}
	for _, c := range resp.Cookies() {
		if c.Name == auth.CookieName {
			t.Fatalf("请求被拒却下发会话 Cookie（%s）", c.Name)
		}
	}
}
