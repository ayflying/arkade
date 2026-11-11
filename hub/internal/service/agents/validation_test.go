package agents

import (
	"strconv"
	"strings"
	"testing"

	"github.com/gogf/gf/v2/errors/gerror"
)

func TestNormalizeDirectAddress(t *testing.T) {
	for _, tt := range []struct{ raw, want string }{
		{"", ""}, {" \t\n", ""}, {" EXAMPLE.Com ", "example.com"},
		{"localhost", "localhost"}, {"my-host.example", "my-host.example"},
		{" 192.168.1.2 ", "192.168.1.2"}, {"127.0.0.1", "127.0.0.1"},
		{"[2001:0DB8:0:0::1]", "2001:db8::1"}, {"::1", "::1"},
		{"[192.168.1.2]", "192.168.1.2"},
		{strings.Repeat("a", 63), strings.Repeat("a", 63)},
		{strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61), strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)},
	} {
		t.Run(tt.raw, func(t *testing.T) {
			got, err := NormalizeDirectAddress(tt.raw)
			if err != nil || got != tt.want {
				t.Fatalf("NormalizeDirectAddress(%q) = %q, %v; want %q, nil", tt.raw, got, err, tt.want)
			}
		})
	}
	for _, raw := range []string{
		"https://example.com", "example.com:443", "user@example.com", "example.com/path",
		"example.com?x", "example.com#x", "example.com\\x", "[example.com]",
		".example.com", "example..com", "example.com.", "-host", "host-", "host_name",
		"例子.com", "host name", "0.0.0.0", "::", "[::]", "224.0.0.1", "ff02::1",
		"fe80::1%eth0", "[fe80::1%eth0]", "[::1", "::1]",
		strings.Repeat("a", 64), strings.Repeat("a.", 127),
	} {
		t.Run(raw, func(t *testing.T) {
			got, err := NormalizeDirectAddress(raw)
			if got != "" || err == nil || err.Error() != "直连地址请填写IP或域名，不含协议、端口和路径" {
				t.Fatalf("NormalizeDirectAddress(%q) = %q, %v", raw, got, err)
			}
			if _, ok := err.(*gerror.Error); !ok {
				t.Fatalf("error type = %T; want *gerror.Error", err)
			}
		})
	}
}

func labelSet(n int) map[string]string {
	labels := make(map[string]string, n)
	for i := 0; i < n; i++ {
		labels[strconv.Itoa(i)] = "value"
	}
	return labels
}

func TestValidateLabels(t *testing.T) {
	for _, tt := range []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{"nil", nil, ""},
		{"empty", map[string]string{}, ""},
		{"count boundary", labelSet(32), ""},
		{"rune boundaries", map[string]string{strings.Repeat("名", 64): strings.Repeat("值", 256)}, ""},
		{"untrimmed lengths accepted", map[string]string{" name ": " value "}, ""},
		{"count", labelSet(33), "标签数量不能超过 32 个"},
		{"blank", map[string]string{" \t\u3000": ""}, "标签名不能为空"},
		{"blank before key length", map[string]string{strings.Repeat(" ", 65): ""}, "标签名不能为空"},
		{"key", map[string]string{strings.Repeat("名", 65): ""}, "标签名不能超过 64 个字符"},
		{"original key length", map[string]string{" " + strings.Repeat("名", 64): ""}, "标签名不能超过 64 个字符"},
		{"key before value", map[string]string{strings.Repeat("名", 65): strings.Repeat("值", 257)}, "标签名不能超过 64 个字符"},
		{"value", map[string]string{"key": strings.Repeat("值", 257)}, "标签值不能超过 256 个字符"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidateLabels(tt.labels); got != tt.want {
				t.Fatalf("ValidateLabels() = %q; want %q", got, tt.want)
			}
		})
	}
	labels := labelSet(33)
	labels[""] = ""
	if got := ValidateLabels(labels); got != "标签数量不能超过 32 个" {
		t.Fatalf("count must precede individual label validation: %q", got)
	}
}

func TestValidateCreate(t *testing.T) {
	for _, tt := range []struct {
		title, name, note string
		labels            map[string]string
		wantName, wantErr string
	}{
		{"trim name", " \t服务器\u3000", "", nil, "服务器", ""},
		{"rune boundaries", " " + strings.Repeat("名", 128) + " ", strings.Repeat("注", 2000), map[string]string{strings.Repeat("键", 64): strings.Repeat("值", 256)}, strings.Repeat("名", 128), ""},
		{"count boundary", "name", "", labelSet(32), "name", ""},
		{"blank name first", " \t", strings.Repeat("x", 2001), labelSet(33), "", "服务器名称不能为空"},
		{"long name before note", strings.Repeat("名", 129), strings.Repeat("x", 2001), labelSet(33), "", "服务器名称过长（最多 128 个字符）"},
		{"note before labels", "name", strings.Repeat("注", 2001), labelSet(33), "", "备注过长（最多 2000 个字符）"},
		{"original note count", "name", " " + strings.Repeat("注", 2000), nil, "", "备注过长（最多 2000 个字符）"},
		{"whitespace note count", "name", strings.Repeat(" ", 2001), nil, "", "备注过长（最多 2000 个字符）"},
		{"label count", "name", "", labelSet(33), "", "标签数量过多（最多 32 个）"},
		{"blank key", "name", "", map[string]string{" \t": ""}, "", "标签的键或值不合法"},
		{"long key", "name", "", map[string]string{strings.Repeat("键", 65): ""}, "", "标签的键或值不合法"},
		{"original key length", "name", "", map[string]string{" " + strings.Repeat("键", 64): ""}, "", "标签的键或值不合法"},
		{"long value", "name", "", map[string]string{"key": strings.Repeat("值", 257)}, "", "标签的键或值不合法"},
	} {
		t.Run(tt.title, func(t *testing.T) {
			got, err := ValidateCreate(tt.name, tt.note, tt.labels)
			if got != tt.wantName {
				t.Fatalf("normalized name = %q; want %q", got, tt.wantName)
			}
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("error = %v; want %q", err, tt.wantErr)
			}
			if _, ok := err.(*gerror.Error); !ok {
				t.Fatalf("error type = %T; want *gerror.Error", err)
			}
		})
	}
	labels := labelSet(33)
	labels[""] = ""
	if _, err := ValidateCreate("name", "", labels); err == nil || err.Error() != "标签数量过多（最多 32 个）" {
		t.Fatalf("count must precede individual label validation: %v", err)
	}
}
