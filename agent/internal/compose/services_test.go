package compose

// services_test.go 覆盖 compose 配置的服务名解析。
//
// 解析器是手写的行解析（不引 YAML 库，理由见 services.go），
// 因此必须覆盖真实世界里各种写法，包括那些"不该被解析出来"的东西。

import (
	"reflect"
	"testing"
)

// TestParseServicesBasic 验证最基本的写法。
func TestParseServicesBasic(t *testing.T) {
	content := `services:
  web:
    image: nginx
  db:
    image: postgres
`
	got := ParseServices(content)
	want := []string{"db", "web"} // 升序
	if !reflect.DeepEqual(got, want) {
		t.Errorf("应为 %v，实际 %v", want, got)
	}
}

// TestParseServicesDeepIndent 验证更深缩进（属性）不会被当成服务名。
func TestParseServicesDeepIndent(t *testing.T) {
	content := `services:
    web:
        image: nginx
        ports:
            - "80:80"
    api:
        image: go:1.24
`
	got := ParseServices(content)
	want := []string{"api", "web"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("应为 %v，实际 %v", want, got)
	}
}

// TestParseServicesStopsAtNextTopKey 验证遇到下一个顶格键就停止。
func TestParseServicesStopsAtNextTopKey(t *testing.T) {
	content := `version: "3"
services:
  web:
    image: nginx
volumes:
  data:
  cache:
networks:
  front:
`
	got := ParseServices(content)
	want := []string{"web"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("只应解析 services 段，实际 %v", got)
	}
}

// TestParseServicesWithComments 验证注释不干扰解析。
func TestParseServicesWithComments(t *testing.T) {
	content := `# 顶部注释
services:
  # 注释行
  web:            # 行尾注释
    image: nginx  # 这也是注释
  db:
    image: postgres
`
	got := ParseServices(content)
	want := []string{"db", "web"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("应为 %v，实际 %v", want, got)
	}
}

// TestParseServicesNoServices 验证没有 services 段时返回空。
func TestParseServicesNoServices(t *testing.T) {
	cases := []string{
		"",
		"version: \"3\"\n",
		"services:\n", // 有段但没内容
		"not: yaml: at all",
	}
	for _, c := range cases {
		if got := ParseServices(c); len(got) != 0 {
			t.Errorf("内容 %q 应解析出 0 个服务，实际 %v", c, got)
		}
	}
}

// TestParseServicesRejectsBadNames 验证非法服务名被过滤。
func TestParseServicesRejectsBadNames(t *testing.T) {
	content := `services:
  good:
    image: nginx
  Bad:
    image: nginx
  "../evil":
    image: nginx
  a b:
    image: nginx
`
	got := ParseServices(content)
	// 大写是合法的 compose 服务名，应保留；带引号与空格的应被过滤。
	want := []string{"Bad", "good"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("应为 %v，实际 %v", want, got)
	}
}

// TestParseServicesInlineSyntax 验证行内写法的处理。
func TestParseServicesInlineSyntax(t *testing.T) {
	// 服务带冒号后跟内容（web: {}）应能取出键名。
	content := `services:
  web: {}
  db:
    image: postgres
`
	got := ParseServices(content)
	want := []string{"db", "web"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("应为 %v，实际 %v", want, got)
	}
}

// TestParseServicesRealWorld 验证一份接近真实的配置。
func TestParseServicesRealWorld(t *testing.T) {
	content := `name: myapp

services:
  web:
    image: nginx:1.27-alpine
    restart: unless-stopped
    ports:
      - "8080:80"
    environment:
      TZ: Asia/Shanghai
    depends_on:
      - api

  api:
    build:
      context: ./api
      dockerfile: Dockerfile
    environment:
      DATABASE_URL: postgres://db:5432/app

  db:
    image: postgres:16-alpine
    volumes:
      - pgdata:/var/lib/postgresql/data

volumes:
  pgdata:
`
	got := ParseServices(content)
	want := []string{"api", "db", "web"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("应为 %v，实际 %v", want, got)
	}
}

// TestIndentWidth 验证缩进计算。
func TestIndentWidth(t *testing.T) {
	cases := map[string]int{
		"":        0,
		"foo":     0,
		"  foo":   2,
		"    foo": 4,
		"\tfoo":   2, // tab 按 2 计
		"  \tfoo": 4,
	}
	for in, want := range cases {
		if got := indentWidth(in); got != want {
			t.Errorf("%q 缩进应为 %d，实际 %d", in, want, got)
		}
	}
}

// TestKeyName 验证键名提取。
func TestKeyName(t *testing.T) {
	ok := map[string]string{
		"web:":            "web",
		"web: {}":         "web",
		"my-svc: image=x": "my-svc",
	}
	for in, want := range ok {
		got, good := keyName(in)
		if !good || got != want {
			t.Errorf("%q 应解析为 %q，实际 %q（ok=%v）", in, want, got, good)
		}
	}
	// 不是键的行。
	for _, in := range []string{"- item", "noColon", ":v", "a b: c"} {
		if _, good := keyName(in); good {
			t.Errorf("%q 不应被识别为键", in)
		}
	}
}
