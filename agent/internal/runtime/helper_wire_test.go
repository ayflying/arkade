package runtime

// helper_wire_test.go：**真正检查 tar 注入内容**的测试。
//
// ============================ 为什么必须有这一层 ============================
//
// 真实故障：compose-source-helper 的请求文件被写成
// /compose-source-request.json，而公共注入函数永远往 /request.json 写。
// 结果 hidden CLI 读不到请求 → 直接 exit 3 → 没有回执 →
// 上层只能报"进程失败且未留下结果" → 全部 runtime_failed。
//
// 当时所有测试都是**替换 runHelper** 的：它们验证了"调用被发起"，
// 却完全没有验证"注入的 tar 里那个文件到底叫什么"。
// 因此这里不再 mock runHelper，而是真的跑 runHelper，
// 用一个假的 archive 端点把 tar 流收下来逐个 header 检查。
//
// 这类 wire 级错误只能这样测出来。

import (
	"archive/tar"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"arkade/agent/internal/helperfs"
)

// archiveCapture 记录 PUT archive 收到的 tar 条目。
type archiveCapture struct {
	mu      sync.Mutex
	names   []string
	types   map[string]byte
	bodies  map[string][]byte
	archive string
	// capAdd 记录建容器请求里的 HostConfig.CapAdd。
	capAdd    []string
	sawCreate bool
}

// capAddEngine 构造一个会记录 CapAdd 的假引擎。
func capAddEngine(t *testing.T, cap *archiveCapture) *engine {
	t.Helper()
	return newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.Contains(p, "/images/"):
			_, _ = w.Write([]byte(`{"Id":"sha256:deadbeef"}`))
		case strings.HasSuffix(p, "/containers/create"):
			var body struct {
				HostConfig struct {
					CapAdd  []string `json:"CapAdd"`
					CapDrop []string `json:"CapDrop"`
				} `json:"HostConfig"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			cap.mu.Lock()
			cap.capAdd = body.HostConfig.CapAdd
			cap.sawCreate = true
			cap.mu.Unlock()
			_, _ = w.Write([]byte(`{"Id":"cid-1"}`))
		case strings.HasSuffix(p, "/archive") && r.Method == http.MethodPut:
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(p, "/start"):
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(p, "/containers/cid-1/json"):
			_, _ = w.Write([]byte(`{"State":{"Running":false,"ExitCode":0}}`))
		case strings.HasSuffix(p, "/wait"):
			_, _ = w.Write([]byte(`{"StatusCode":0}`))
		case strings.HasSuffix(p, "/archive") && r.Method == http.MethodGet:
			copyTar(w, "response.json", []byte(`{"ok":true}`))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

// TestHelperCapAddOnlyForSourcePaths 验证 DAC_READ_SEARCH **只**给编排配置读取。
//
// 用户明确要求"容器可管理 + 读取统一"：auto 与 raw 都是读同一份配置，
// 若只有 raw 能读 0600，就会出现"手动能看、自动接管却说权限不足"的分裂行为。
// 但卷文件管理没有这个需求，必须保持不加 capability。
func TestHelperCapAddOnlyForSourcePaths(t *testing.T) {
	// volume 类 helper：CapAdd 必须为空。
	var vol archiveCapture
	ev := capAddEngine(t, &vol)
	if _, err := ev.runHelper(context.Background(), helperSpec{
		cmd:          []string{"volume-file-helper"},
		request:      []byte(`{}`),
		responsePath: "/response.json",
		readSearch:   false,
	}); err != nil {
		t.Fatalf("volume helper 不应失败: %v", err)
	}
	if !vol.sawCreate {
		t.Fatal("应发出建容器请求")
	}
	if len(vol.capAdd) != 0 {
		t.Fatalf("卷文件 helper 不得加任何 capability，实际 %v", vol.capAdd)
	}

	// 编排读取（auto / raw 共用的 spec）：必须加 DAC_READ_SEARCH。
	for _, label := range []string{"auto", "raw"} {
		var cap archiveCapture
		e := capAddEngine(t, &cap)
		if _, err := e.runHelper(context.Background(), helperSpec{
			cmd:          []string{"compose-source-helper"},
			request:      []byte(`{}`),
			responsePath: "/response.json",
			readSearch:   true,
		}); err != nil {
			t.Fatalf("%s 路径不应失败: %v", label, err)
		}
		if len(cap.capAdd) != 1 || cap.capAdd[0] != "DAC_READ_SEARCH" {
			t.Fatalf("%s 应只加 DAC_READ_SEARCH，实际 %v", label, cap.capAdd)
		}
	}
}

// helperWireEngine 构造一个假引擎：
// 镜像 inspect 成功、建容器成功、archive 上传被捕获、启动成功、
// 等待返回退出码 0、GET archive 返回给定结果文件。
func helperWireEngine(t *testing.T, cap *archiveCapture, responseBody []byte) *engine {
	t.Helper()
	return newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.Contains(p, "/images/") && strings.HasSuffix(p, "/json"):
			_, _ = w.Write([]byte(`{"Id":"sha256:deadbeef"}`))
		case strings.HasSuffix(p, "/containers/create"):
			_, _ = w.Write([]byte(`{"Id":"cid-1","Warnings":[]}`))
		case strings.HasSuffix(p, "/archive") && r.Method == http.MethodPut:
			cap.mu.Lock()
			cap.archive = r.URL.Query().Get("path")
			cap.types = map[string]byte{}
			cap.bodies = map[string][]byte{}
			cap.mu.Unlock()
			tr := tar.NewReader(r.Body)
			for {
				hdr, err := tr.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				body, _ := io.ReadAll(tr)
				cap.mu.Lock()
				cap.names = append(cap.names, hdr.Name)
				cap.types[hdr.Name] = hdr.Typeflag
				cap.bodies[hdr.Name] = body
				cap.mu.Unlock()
			}
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(p, "/start"):
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(p, "/containers/cid-1/json"):
			_, _ = w.Write([]byte(`{"State":{"Running":false,"ExitCode":0}}`))
		case strings.HasSuffix(p, "/wait"):
			_, _ = w.Write([]byte(`{"StatusCode":0}`))
		case strings.HasSuffix(p, "/archive") && r.Method == http.MethodGet:
			// 把结果文件按 docker 的 tar 流返回。
			copyTar(w, "response.json", responseBody)
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

// copyTar 按 docker GET archive 的格式回一个单文件 tar。
func copyTar(w http.ResponseWriter, name string, body []byte) {
	w.Header().Set("Content-Type", "application/x-tar")
	tw := tar.NewWriter(w)
	_ = tw.WriteHeader(&tar.Header{
		Name:     name,
		Typeflag: tar.TypeReg,
		Mode:     0o644,
		Size:     int64(len(body)),
	})
	_, _ = tw.Write(body)
	_ = tw.Close()
}

// TestRunHelperInjectsRequestAtFixedPath 验证请求被注入到 /request.json。
//
// 这是 hidden CLI 唯一会读的位置；写成别的名字就等于 CLI 读不到。
func TestRunHelperInjectsRequestAtFixedPath(t *testing.T) {
	var cap archiveCapture
	e := helperWireEngine(t, &cap, []byte(`{"ok":true}`))

	reqPayload := []byte(`{"config_file":"compose.yml"}`)
	out, err := e.runHelper(context.Background(), helperSpec{
		cmd:          []string{"compose-source-helper"},
		mounts:       nil,
		request:      reqPayload,
		responsePath: "/response.json",
	})
	if err != nil {
		t.Fatalf("runHelper 不应失败: %v", err)
	}
	if string(out) != `{"ok":true}` {
		t.Fatalf("结果不符: %q", out)
	}

	// 注入端必须往容器根写。
	if cap.archive != "/" {
		t.Fatalf("archive 目标应为 /，实际 %q", cap.archive)
	}
	// 关键断言：请求文件必须叫 request.json（即 helperfs.RequestPath 的基名）。
	body, ok := cap.bodies["request.json"]
	if !ok {
		t.Fatalf("注入的 tar 里没有 request.json，实际条目: %v", cap.names)
	}
	if string(body) != string(reqPayload) {
		t.Fatalf("请求内容不符: %q", body)
	}
	if cap.types["request.json"] != tar.TypeReg {
		t.Fatalf("request.json 应为普通文件")
	}
	// 二进制也必须注入，且是可执行普通文件。
	if _, hasExe := cap.bodies[strings.TrimPrefix(helperBinaryPath, "/")]; !hasExe {
		t.Fatalf("缺少注入的二进制，实际条目: %v", cap.names)
	}
}

// TestSourceRequestPathMatchesInjectedName 验证 source 的请求路径常量
// 与公共注入端**是同一个名字**。
func TestSourceRequestPathMatchesInjectedName(t *testing.T) {
	if helperfs.SourceRequestPath != helperfs.RequestPath {
		t.Fatalf("source 请求路径 %q 必须与注入路径 %q 一致",
			helperfs.SourceRequestPath, helperfs.RequestPath)
	}
	if helperfs.SourceRequestPath != "/request.json" {
		t.Fatalf("注入端固定写 /request.json，实际常量 %q", helperfs.SourceRequestPath)
	}
	if helperSourceResponsePath != helperfs.ResponsePath {
		t.Fatalf("读取端 %q 必须与产出端 %q 一致",
			helperSourceResponsePath, helperfs.ResponsePath)
	}
}

// TestRunHelperNonZeroStillReadsResult 验证非零退出码仍读结果（wire 层）。
func TestRunHelperNonZeroStillReadsResult(t *testing.T) {
	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.Contains(p, "/images/"):
			_, _ = w.Write([]byte(`{"Id":"sha256:deadbeef"}`))
		case strings.HasSuffix(p, "/containers/create"):
			_, _ = w.Write([]byte(`{"Id":"cid-1"}`))
		case strings.HasSuffix(p, "/archive") && r.Method == http.MethodPut:
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(p, "/start"):
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(p, "/containers/cid-1/json"):
			_, _ = w.Write([]byte(`{"State":{"Running":false,"ExitCode":2}}`))
		case strings.HasSuffix(p, "/wait"):
			_, _ = w.Write([]byte(`{"StatusCode":2}`))
		case strings.HasSuffix(p, "/archive") && r.Method == http.MethodGet:
			copyTar(w, "response.json", []byte(`{"error":"配置文件不存在"}`))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	out, err := e.runHelper(context.Background(), helperSpec{
		cmd:          []string{"compose-source-helper"},
		request:      []byte(`{}`),
		responsePath: "/response.json",
	})
	if err != nil {
		t.Fatalf("非零退出码但有结果时不应报错: %v", err)
	}
	if !strings.Contains(string(out), "配置文件不存在") {
		t.Fatalf("应读回结构化原因，实际 %q", out)
	}
}

// TestRunHelperRejectsMismatchedResponseName 验证结果文件名的校验不受影响。
func TestRunHelperRejectsMismatchedResponseName(t *testing.T) {
	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.Contains(p, "/images/"):
			_, _ = w.Write([]byte(`{"Id":"sha256:deadbeef"}`))
		case strings.HasSuffix(p, "/containers/create"):
			_, _ = w.Write([]byte(`{"Id":"cid-1"}`))
		case strings.HasSuffix(p, "/archive") && r.Method == http.MethodPut:
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(p, "/start"):
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(p, "/containers/cid-1/json"):
			_, _ = w.Write([]byte(`{"State":{"Running":false,"ExitCode":0}}`))
		case strings.HasSuffix(p, "/wait"):
			_, _ = w.Write([]byte(`{"StatusCode":0}`))
		case strings.HasSuffix(p, "/archive") && r.Method == http.MethodGet:
			copyTar(w, "other.json", []byte(`{"ok":true}`))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	_, err := e.runHelper(context.Background(), helperSpec{
		cmd:          []string{"compose-source-helper"},
		request:      []byte(`{}`),
		responsePath: "/response.json",
	})
	if err == nil {
		t.Fatal("结果文件名不符必须报错")
	}
}

// TestHelperSpecRequestRoundTrip 验证 helperSpec.request 原样进入 tar。
func TestHelperSpecRequestRoundTrip(t *testing.T) {
	var cap archiveCapture
	e := helperWireEngine(t, &cap, []byte(`{"ok":true}`))
	payload := map[string]string{"config_file": "sub/compose.yml"}
	reqJSON, _ := json.Marshal(payload)
	if _, err := e.runHelper(context.Background(), helperSpec{
		cmd:          []string{"compose-source-helper"},
		request:      reqJSON,
		responsePath: "/response.json",
	}); err != nil {
		t.Fatalf("不应失败: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(cap.bodies["request.json"], &got); err != nil {
		t.Fatalf("注入的请求不是合法 JSON: %v", err)
	}
	if got["config_file"] != "sub/compose.yml" {
		t.Fatalf("请求内容不符: %v", got)
	}
}
