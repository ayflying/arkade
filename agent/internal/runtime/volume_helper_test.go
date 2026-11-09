package runtime

// volume_helper_test.go：用 httptest 模拟引擎的 unix 套接字，
// 覆盖 helper 生命周期与结果解析。
//
// 这些用例不碰真 Docker：它们校验的是"我们发给引擎的请求长什么样"
// 以及"引擎返回各种响应时我们怎么判定"，这正是重复创建返回 503
// 那类 bug 的所在层。

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// helperEngine 构造一个指向测试服务器的 engine（复用 newTestEngine）。
func helperEngine(t *testing.T, h http.Handler) *engine {
	t.Helper()
	return newTestEngine(t, func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) })
}

// helperTar 构造一个只含指定文件内容的 tar 流。
func helperTar(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestHelperNonZeroExitStillReadsResponse 是本轮修复的核心回归用例。
//
// helper 业务失败时（如 mkdir 目标已存在）会写 response.json 并返回非零退出码。
// 若调用方看到非零就直接报错，结构化错误码就被丢弃，
// handler 只能落到兜底 runtime_failed(503)——
// 而"已存在"应该是 conflict(409)。
func TestHelperNonZeroExitStillReadsResponse(t *testing.T) {
	// helper 写出的错误结果。
	payload := []byte(`{"error":"目标已存在","code":"exists"}`)
	archive := helperTar(t, "response.json", payload)
	var removed int32

	e := helperEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case p == "/volumes/v":
			_, _ = w.Write([]byte(`{"Name":"v","Driver":"local","Mountpoint":"/var/lib/docker/volumes/v/_data"}`))
		case strings.HasSuffix(p, "/images/alpine:3.20/json"):
			_, _ = w.Write([]byte(`{"Id":"sha256:img"}`))
		case p == "/containers/create":
			_, _ = w.Write([]byte(`{"Id":"cid"}`))
		case r.Method == http.MethodPut && strings.HasSuffix(p, "/archive"):
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(p, "/start"):
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(p, "/json"):
			// 已退出，退出码 2 = 业务校验失败。
			_, _ = w.Write([]byte(`{"State":{"Running":false,"ExitCode":2}}`))
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/archive"):
			w.Header().Set("Content-Type", "application/x-tar")
			_, _ = w.Write(archive)
		case r.Method == http.MethodDelete:
			atomic.AddInt32(&removed, 1)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	out, err := e.RunVolumeFileHelper(context.Background(), "v",
		map[string]any{"operation": "mkdir", "path": "d"})
	if err != nil {
		t.Fatalf("非零退出码但有合法 JSON 结果时应返回结果，实际错误: %v", err)
	}
	if string(out) != string(payload) {
		t.Fatalf("结果不符: %s", out)
	}
	// 容器必须被清理。
	if atomic.LoadInt32(&removed) == 0 {
		t.Fatal("helper 容器未被删除")
	}
}

// TestHelperNonZeroExitWithoutResponseFails 验证读不到结果时才用退出码判失败。
func TestHelperNonZeroExitWithoutResponseFails(t *testing.T) {
	e := helperEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case p == "/volumes/v":
			_, _ = w.Write([]byte(`{"Name":"v","Driver":"local","Mountpoint":"/var/lib/docker/volumes/v/_data"}`))
		case strings.HasSuffix(p, "/images/alpine:3.20/json"):
			_, _ = w.Write([]byte(`{"Id":"sha256:img"}`))
		case p == "/containers/create":
			_, _ = w.Write([]byte(`{"Id":"cid"}`))
		case r.Method == http.MethodPut:
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(p, "/start"):
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(p, "/json"):
			_, _ = w.Write([]byte(`{"State":{"Running":false,"ExitCode":3}}`))
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/archive"):
			// 没有 response.json：模拟中途崩溃。
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	if _, err := e.RunVolumeFileHelper(context.Background(), "v",
		map[string]any{"operation": "list", "path": ""}); err == nil {
		t.Fatal("无结果且非零退出码必须报错")
	}
}

// TestHelperRejectsWrongArchiveName 验证归档里不是 response.json 时拒绝。
func TestHelperRejectsWrongArchiveName(t *testing.T) {
	archive := helperTar(t, "something-else.json", []byte(`{"ok":true}`))
	e := helperEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case p == "/volumes/v":
			_, _ = w.Write([]byte(`{"Name":"v","Driver":"local","Mountpoint":"/var/lib/docker/volumes/v/_data"}`))
		case strings.HasSuffix(p, "/images/alpine:3.20/json"):
			_, _ = w.Write([]byte(`{"Id":"sha256:img"}`))
		case p == "/containers/create":
			_, _ = w.Write([]byte(`{"Id":"cid"}`))
		case r.Method == http.MethodPut:
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(p, "/start"):
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(p, "/json"):
			_, _ = w.Write([]byte(`{"State":{"Running":false,"ExitCode":0}}`))
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/archive"):
			_, _ = w.Write(archive)
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	if _, err := e.RunVolumeFileHelper(context.Background(), "v",
		map[string]any{"operation": "list"}); err == nil {
		t.Fatal("归档文件名不符必须报错")
	}
}

// TestHelperResponseSizeLimit 验证超过上限的响应被拒（不静默截断）。
func TestHelperResponseSizeLimit(t *testing.T) {
	big := make([]byte, maxHelperResponseBytes+1)
	archive := helperTar(t, "response.json", big)
	e := helperEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case p == "/volumes/v":
			_, _ = w.Write([]byte(`{"Name":"v","Driver":"local","Mountpoint":"/var/lib/docker/volumes/v/_data"}`))
		case strings.HasSuffix(p, "/images/alpine:3.20/json"):
			_, _ = w.Write([]byte(`{"Id":"sha256:img"}`))
		case p == "/containers/create":
			_, _ = w.Write([]byte(`{"Id":"cid"}`))
		case r.Method == http.MethodPut:
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(p, "/start"):
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(p, "/json"):
			_, _ = w.Write([]byte(`{"State":{"Running":false,"ExitCode":0}}`))
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/archive"):
			_, _ = w.Write(archive)
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	if _, err := e.RunVolumeFileHelper(context.Background(), "v",
		map[string]any{"operation": "read", "path": "a"}); err == nil {
		t.Fatal("超过上限的响应必须报错")
	}
}

// TestHelperContainerSpecSecurity 验证创建容器时的关键安全字段。
//
// 这些字段一旦丢失，helper 就从"受限子进程"退化成"特权浏览器"，
// 而且从行为上很难发现，只能在请求体上断言。
func TestHelperContainerSpecSecurity(t *testing.T) {
	var cfg map[string]any
	e := helperEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case p == "/volumes/v":
			_, _ = w.Write([]byte(`{"Name":"v","Driver":"local","Mountpoint":"/var/lib/docker/volumes/v/_data"}`))
		case strings.HasSuffix(p, "/images/alpine:3.20/json"):
			_, _ = w.Write([]byte(`{"Id":"sha256:img"}`))
		case p == "/containers/create":
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &cfg)
			_, _ = w.Write([]byte(`{"Id":"cid"}`))
		case r.Method == http.MethodPut:
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(p, "/start"):
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(p, "/json"):
			_, _ = w.Write([]byte(`{"State":{"Running":false,"ExitCode":0}}`))
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/archive"):
			_, _ = w.Write(helperTar(t, "response.json", []byte(`{"path":""}`)))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	if _, err := e.RunVolumeFileHelper(context.Background(), "v",
		map[string]any{"operation": "list"}); err != nil {
		t.Fatalf("不应失败: %v", err)
	}

	// 镜像必须用 ID 而不是 tag（tag 可被并发 retag）。
	if cfg["Image"] != "sha256:img" {
		t.Fatalf("Image 应为镜像 ID，实际 %v", cfg["Image"])
	}
	// Entrypoint 必须固定为注入的二进制，不能继承镜像的 ENTRYPOINT。
	ep, _ := cfg["Entrypoint"].([]any)
	if len(ep) != 1 || ep[0] != helperBinaryPath {
		t.Fatalf("Entrypoint 未固定: %v", cfg["Entrypoint"])
	}
	// User 必须覆盖为 root：镜像 USER 不可信。
	if cfg["User"] != "0:0" {
		t.Fatalf("User 应为 0:0，实际 %v", cfg["User"])
	}

	hc, _ := cfg["HostConfig"].(map[string]any)
	if hc == nil {
		t.Fatal("缺少 HostConfig")
	}
	if hc["NetworkMode"] != "none" {
		t.Fatalf("NetworkMode 应为 none: %v", hc["NetworkMode"])
	}
	if hc["Privileged"] != false {
		t.Fatalf("不能是特权容器: %v", hc["Privileged"])
	}
	caps, _ := hc["CapDrop"].([]any)
	if len(caps) != 1 || caps[0] != "ALL" {
		t.Fatalf("CapDrop 应为 ALL: %v", hc["CapDrop"])
	}
	if hc["Memory"] != float64(192<<20) {
		t.Fatalf("Memory 应为 192MiB: %v", hc["Memory"])
	}
	// bind mount 且不自动创建挂载点。
	mounts, _ := hc["Mounts"].([]any)
	if len(mounts) != 1 {
		t.Fatalf("应只有一个挂载: %v", mounts)
	}
	m := mounts[0].(map[string]any)
	if m["Type"] != "bind" || m["Target"] != helperDataMount {
		t.Fatalf("挂载类型/目标不符: %v", m)
	}
	bo, _ := m["BindOptions"].(map[string]any)
	if bo == nil || bo["CreateMountpoint"] != false {
		t.Fatalf("CreateMountpoint 必须为 false: %v", m["BindOptions"])
	}
	// 只读操作必须是只读挂载。
	if m["ReadOnly"] != true {
		t.Fatalf("list 应为只读挂载: %v", m["ReadOnly"])
	}
}

// TestHelperBusyWhenConcurrentLimitReached 验证并发闸门真的生效。
//
// 这是 Lead 要求确认的点：上限必须是实际可运行的，
// 而不是只写在文档里。
func TestHelperBusyWhenConcurrentLimitReached(t *testing.T) {
	// 占满所有槽位。
	for i := 0; i < maxHelperConcurrent; i++ {
		helperSem <- struct{}{}
	}
	defer func() {
		for i := 0; i < maxHelperConcurrent; i++ {
			<-helperSem
		}
	}()

	e := helperEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("并发已满时不应发出任何引擎请求: %s", r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))

	_, err := e.RunVolumeFileHelper(context.Background(), "v", map[string]any{"operation": "list"})
	if err == nil {
		t.Fatal("并发已满时必须报 busy")
	}
	if !isBusy(err) {
		t.Fatalf("期望 ErrHelperBusy，实际 %v", err)
	}
}

// isBusy 判断错误是否为 busy。
func isBusy(err error) bool { return err == ErrHelperBusy }

// TestHelperRejectsNonLocalOrOptionedVolume 验证只支持普通 local 卷。
func TestHelperRejectsNonLocalOrOptionedVolume(t *testing.T) {
	cases := map[string]string{
		"有挂载选项": `{"Name":"v","Driver":"local","Mountpoint":"/m","Options":{"o":"bind"}}`,
		"远程驱动":  `{"Name":"v","Driver":"nfs","Mountpoint":"/m"}`,
		"根路径":   `{"Name":"v","Driver":"local","Mountpoint":"/"}`,
		"相对路径":  `{"Name":"v","Driver":"local","Mountpoint":"m"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			e := helperEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/volumes/v" {
					_, _ = w.Write([]byte(body))
					return
				}
				t.Errorf("不应继续后续请求: %s", r.URL.Path)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			if _, err := e.RunVolumeFileHelper(context.Background(), "v",
				map[string]any{"operation": "list"}); err == nil {
				t.Fatal("必须拒绝")
			}
		})
	}
}
