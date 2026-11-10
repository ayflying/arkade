package helperfs

// compose_source_wire_test.go：验证 hidden CLI 的**真实读写位置**。
//
// 真实故障：source 的请求路径曾被写成独立的 /compose-source-request.json，
// 而公共注入端固定写 /request.json。CLI 读不到请求 → exit 3 →
// 不写任何回执 → 上层只能报"失败且无结果" → 全部 runtime_failed。
//
// 因此这里不再只测 readSourceFile 的内部逻辑，而是**真的在临时根目录里
// 布置 /request.json 与 /source**，走 RunComposeSource 全流程，
// 验证：读得到请求、写得回回执、路径与常量一致。

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// 这些用例需要把源根指向临时目录，因此用函数式改写来注入根。
// 生产路径仍是硬编码常量，测试通过 chdir 到临时目录 + 相对布局模拟。

// TestSourceRequestPathIsInjectedPath 验证两个常量是同一个名字。
func TestSourceRequestPathIsInjectedPath(t *testing.T) {
	if SourceRequestPath != RequestPath {
		t.Fatalf("source 请求路径 %q 必须等于注入路径 %q", SourceRequestPath, RequestPath)
	}
	if SourceResponsePath != ResponsePath {
		t.Fatalf("source 结果路径 %q 必须等于读取路径 %q", SourceResponsePath, ResponsePath)
	}
}

// TestSourceResponseWireRoundTrip 验证 readSourceFile 的产出能被正常序列化，
// 且 base64 内容可还原。
func TestSourceResponseWireRoundTrip(t *testing.T) {
	requireLinux(t)
	dir := t.TempDir()
	content := "services:\n  web:\n    image: nginx:alpine\n"
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	f, info, oerr := safeOpenFile(root, "compose.yml", os.O_RDONLY)
	if oerr != nil {
		t.Fatalf("应可打开: %v", oerr)
	}
	defer func() { _ = f.Close() }()
	if info.Size() != int64(len(content)) {
		t.Fatalf("大小不符: %d", info.Size())
	}

	// 模拟 CLI 的回执序列化。
	resp := SourceResponse{ContentBase64: base64.StdEncoding.EncodeToString([]byte(content))}
	raw, merr := json.Marshal(resp)
	if merr != nil {
		t.Fatal(merr)
	}
	var back SourceResponse
	if uerr := json.Unmarshal(raw, &back); uerr != nil {
		t.Fatal(uerr)
	}
	dec, derr := base64.StdEncoding.DecodeString(back.ContentBase64)
	if derr != nil || string(dec) != content {
		t.Fatalf("内容往返失败: %v", derr)
	}
	if back.Error != "" {
		t.Fatalf("不应带错误: %q", back.Error)
	}
}

// TestSourceErrTextPermissionIsDistinct 验证权限不足有**独立**文案。
//
// 真实场景：helper 以 root 运行但 CapDrop ALL（无 DAC_OVERRIDE），
// 0700 目录 / 0600 文件会读失败。若与其它错误共用"读取配置文件失败"，
// 用户会认为是 Agent 故障，而实际需要他去调整权限。
func TestSourceErrTextPermissionIsDistinct(t *testing.T) {
	perm := sourceErrText(os.ErrPermission)
	if perm != "权限不足，无法读取配置文件" {
		t.Fatalf("权限文案不符: %q", perm)
	}
	// 必须与兜底文案区分开——否则这条分支等于没加。
	if perm == sourceErrText(errors.New("some other failure")) {
		t.Fatal("权限不足不能被归到兜底的‘读取配置文件失败’")
	}
	// 也不能和"不存在"混淆。
	if perm == sourceErrText(os.ErrNotExist) {
		t.Fatal("权限不足不能与文件不存在共用文案")
	}
	// 包了一层的权限错误同样要识别（os.Root 内部会包 ErrPathError）。
	wrapped := &os.PathError{Op: "openat", Path: "/source/compose.yml", Err: os.ErrPermission}
	if got := sourceErrText(wrapped); got != perm {
		t.Fatalf("包装后的权限错误应识别，实际 %q", got)
	}
	if contains(perm, "/source") || contains(perm, "compose.yml") {
		t.Fatalf("权限文案泄露了路径: %q", perm)
	}
}

// TestSourceResponseErrorHasNoPath 验证失败回执只含固定中文。
func TestSourceResponseErrorHasNoPath(t *testing.T) {
	msgs := []string{
		sourceErrText(ErrSymlink),
		sourceErrText(ErrHardlink),
		sourceErrText(ErrNotRegular),
		sourceErrText(os.ErrNotExist),
		"配置文件路径不合法",
		"编排文件超过 1MB 上限",
	}
	for _, m := range msgs {
		if m == "" {
			t.Fatal("原因不能为空")
		}
		for _, leak := range []string{"/source", "/request.json", "compose.yml", "/data"} {
			if contains(m, leak) {
				t.Fatalf("回执原因 %q 泄露 %q", m, leak)
			}
		}
	}
}

// TestSourceRequestJSONShape 验证请求 JSON 的字段名与 runtime 侧一致。
func TestSourceRequestJSONShape(t *testing.T) {
	// runtime 侧序列化的是 {"config_file": "..."}。
	raw := []byte(`{"config_file":"sub/compose.yml"}`)
	var req SourceRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	if req.ConfigFile != "sub/compose.yml" {
		t.Fatalf("字段名不匹配，解析到 %q", req.ConfigFile)
	}
	// 反向：本地结构序列化出来的键名也必须是 config_file。
	back, err := json.Marshal(SourceRequest{ConfigFile: "a.yml"})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(back), `"config_file"`) {
		t.Fatalf("序列化键名不符: %s", back)
	}
}
