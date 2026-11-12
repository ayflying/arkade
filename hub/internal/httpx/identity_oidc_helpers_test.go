package httpx

// identity_oidc_helpers_test.go 放 OIDC 测试共用的请求工具。
//
// 这里只有"怎么发请求、怎么读响应"，没有任何业务断言，
// 断言分在 login / security / settings 三个文件。
// 拆出来的目的：让改断言的人不必翻环境装配，也避免单文件过长。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// do 发一个请求。body 为空表示无请求体；cookies 为 nil 表示不带凭据。
func (e *oidcTestEnv) do(t *testing.T, method, path, body string, cookies []*http.Cookie, headers map[string]string) *http.Response {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, e.base+path, r)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("请求 %s %s 失败: %v", method, path, err)
	}
	return resp
}

// get 是无请求体的 do。
func (e *oidcTestEnv) get(t *testing.T, path string, cookies []*http.Cookie) *http.Response {
	t.Helper()
	return e.do(t, http.MethodGet, path, "", cookies, nil)
}

// post 把 payload 序列化成 JSON 后发出。
func (e *oidcTestEnv) post(t *testing.T, path string, payload any, cookies []*http.Cookie) *http.Response {
	t.Helper()
	raw := ""
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("序列化请求体失败: %v", err)
		}
		raw = string(b)
	}
	return e.do(t, http.MethodPost, path, raw, cookies, nil)
}

// body 读完并关闭响应体。
func (e *oidcTestEnv) body(resp *http.Response) string {
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// jsonBody 读出 JSON 到 map。
func (e *oidcTestEnv) jsonBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	raw := e.body(resp)
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("响应不是 JSON: %s", raw)
	}
	return out
}

// findCookie 在响应里找指定名字的 Cookie。
func findCookie(resp *http.Response, name string) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// pathAndQuery 从绝对 URL 里取出 path?query。
func pathAndQuery(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("解析 URL 失败: %v", err)
	}
	if u.RawQuery == "" {
		return u.Path
	}
	return u.Path + "?" + u.RawQuery
}

// mustJSON 把对象序列化成字符串，失败即终止测试。
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	return string(b)
}
