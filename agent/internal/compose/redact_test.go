package compose

// redact_test.go：脱敏的正确性与 fail-closed 边界。
//
// 这些测试的价值不在"能遮住长密码"，而在**遮住短密码与各种隐蔽写法**：
//   - 行内注释会让天真实现把值解析长，从而漏遮真实短值；
//   - 插值/转义/多行值无法在不实现完整 dotenv 的前提下确定，必须 fail-closed；
//   - 编排正文里的内联密码（environment 列表/flow/块标量）与 .env 无关，
//     只遮 .env 完全保护不到。

import (
	"strings"
	"testing"
)

// TestEnvValuesStripsInlineComment 验证行内注释不进入值。
//
// 天真实现（按第一个 = 切开、TrimQuotes）会得到 "secret # comment"，
// 于是真实的短值 "secret" 在输出里完全不被替换。
func TestEnvValuesStripsInlineComment(t *testing.T) {
	vals, ok := envValues("PASSWORD=secret # comment\nPORT=8080\n")
	if !ok {
		t.Fatal("应能解析")
	}
	if !containsStr(vals, "secret") {
		t.Fatalf("必须提取到真实短值 secret，实际 %v", vals)
	}
	for _, v := range vals {
		if strings.Contains(v, "#") {
			t.Fatalf("注释不应进入值: %q", v)
		}
	}
}

// TestEnvValuesPlainQuoted 验证简单引号值被正确剥离。
func TestEnvValuesPlainQuoted(t *testing.T) {
	vals, ok := envValues("PASSWORD=\"plainpass\"\nTOKEN='tok123'\n")
	if !ok {
		t.Fatal("应能解析")
	}
	for _, want := range []string{"plainpass", "tok123"} {
		if !containsStr(vals, want) {
			t.Fatalf("应提取 %q，实际 %v", want, vals)
		}
	}
}

// TestEnvValuesRejectsInterpolation 验证任何 $ 插值写法被拒绝（fail-closed）。
//
// compose 对 ${VAR}、$VAR、$(...) 都做插值。不实现插值引擎就无法知道
// 真实值，拒绝是唯一安全的选择——漏遮一个就是明文密码进浏览器。
func TestEnvValuesRejectsInterpolation(t *testing.T) {
	cases := []string{
		"PASSWORD=${OTHER}",
		"PASSWORD=$PLAINVAR",
		"PASSWORD=pre$VAR",
		"PASSWORD=$(hostname)",
		"PASSWORD=\"${OTHER}\"",
		"PASSWORD='$OTHER'",
	}
	for _, content := range cases {
		if _, ok := envValues(content); ok {
			t.Fatalf("含 $ 的值必须拒绝（fail-closed）: %q", content)
		}
	}
}

// TestEnvValuesRejectsEscapes 验证含转义的值被拒绝。
//
// 转义后的真实值与字面形态不同，不实现 dotenv 就无法确定。
func TestEnvValuesRejectsEscapes(t *testing.T) {
	cases := []string{
		`PASSWORD="a\tb"`,
		`PASSWORD="a\"b"`,
		`PASSWORD=a\nb`,
	}
	for _, content := range cases {
		if _, ok := envValues(content); ok {
			t.Fatalf("含转义的值必须拒绝: %q", content)
		}
	}
}

// TestEnvValuesRejectsUnparseable 验证无法确定的写法被拒绝。
func TestEnvValuesRejectsUnparseable(t *testing.T) {
	cases := []string{
		"不是赋值行",
		"PASSWORD=\"未闭合",
		"=novalue",
		"PASSWORD='unclosed",
	}
	for _, content := range cases {
		if _, ok := envValues(content); ok {
			t.Fatalf("不确定的写法必须拒绝: %q", content)
		}
	}
}

// TestRedactorMasksShortSecrets 验证**短值也必须遮**。
//
// 只遮长度 >= 6 的值是很常见的错误，而密码完全可能是 "aX9"。
func TestRedactorMasksShortSecrets(t *testing.T) {
	r := newRedactor([]string{"aX9", "1"})
	if got := r.redact("auth failed for aX9"); strings.Contains(got, "aX9") {
		t.Fatalf("短密码漏遮: %q", got)
	}
	if got := r.redact("retry count 1"); strings.Contains(got, "1") {
		t.Fatalf("单字符值也必须遮: %q", got)
	}
}

// TestRedactorMasksRegistryCredentials 验证 URL 内嵌凭证被替换。
func TestRedactorMasksRegistryCredentials(t *testing.T) {
	r := newRedactor(nil)
	got := r.redact("pulling from https://user:supersecret@registry.example.com/v2/")
	if strings.Contains(got, "supersecret") || strings.Contains(got, "user:") {
		t.Fatalf("registry 凭证漏遮: %q", got)
	}
	if !strings.Contains(got, "registry.example.com") {
		t.Fatalf("主机名应保留（便于排查）: %q", got)
	}
}

// TestComposeInlineSecrets 验证编排正文里内联的敏感值被提取。
//
// 覆盖三种常见的 environment 写法：
//   - map 形态 DB_PASSWORD: hunter2
//   - 列表形态 - API_TOKEN=tok123（**最容易漏的一种**）
//   - flow style environment: {SECRET_KEY: flowval}
func TestComposeInlineSecrets(t *testing.T) {
	const cfg = `
services:
  db:
    image: postgres
    environment:
      DB_PASSWORD: hunter2
  web:
    image: nginx
    environment:
      - API_TOKEN=tok123
      - DEBUG=true
  cache:
    image: redis
    environment: {SECRET_KEY: flowval}
`
	vals, err := composeInlineSecrets(cfg)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	for _, want := range []string{"hunter2", "tok123", "flowval"} {
		if !containsStr(vals, want) {
			t.Fatalf("应提取内联敏感值 %q，实际 %v", want, vals)
		}
	}
}

// TestComposeInlineSecretsMultiline 验证块标量里的敏感值被提取。
func TestComposeInlineSecretsMultiline(t *testing.T) {
	const cfg = `
services:
  db:
    image: postgres
    environment:
      DB_PASSWORD: |
        blocksecret
`
	vals, err := composeInlineSecrets(cfg)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	found := false
	for _, v := range vals {
		if strings.Contains(v, "blocksecret") {
			found = true
		}
	}
	if !found {
		t.Fatalf("块标量值应被提取，实际 %v", vals)
	}
}

// TestComposeInlineSecretsInvalidYAML 验证正文解析失败返回错误（调用方 fail-closed）。
func TestComposeInlineSecretsInvalidYAML(t *testing.T) {
	if _, err := composeInlineSecrets("services: [未闭合\n  bad: : :"); err == nil {
		t.Fatal("非法 YAML 应返回错误")
	}
}

// TestSanitizeLineHidesErrorDetail 验证配置错误行被整行隐藏。
//
// 即使字面值替换没覆盖到，这类行也可能残留敏感片段，
// 因此判定为"错误详情"时直接替换整行。
func TestSanitizeLineHidesErrorDetail(t *testing.T) {
	r := newRedactor(nil)
	cases := []string{
		`error while interpolating services.db.environment.DB_PASSWORD: required variable DB_PASSWORD is missing a value`,
		`invalid environment variable reference in service db`,
	}
	for _, line := range cases {
		got := sanitizeLine(line, r)
		if got != noiseLinePlaceholder {
			t.Fatalf("敏感错误行应整行隐藏，输入 %q 得到 %q", line, got)
		}
	}
	// 普通进度行不受影响。
	normal := "db Pulling  abc123 1.2MB/5.0MB"
	if got := sanitizeLine(normal, r); got != normal {
		t.Fatalf("普通输出不应被改写: %q", got)
	}
}

// containsStr 判断切片是否包含给定字符串。
func containsStr(vals []string, want string) bool {
	for _, v := range vals {
		if v == want {
			return true
		}
	}
	return false
}
