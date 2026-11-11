package handler

// image_archive_test.go 覆盖可复用归档处理器的**错误码契约**。
//
// ============================ 为什么重点测错误码 ============================
//
// Hub 侧（task-14）要根据 Agent 返回的错误码决定下一步动作：
//
//	not_found    → 重新导出（归档已过期/被清理）
//	conflict     → 流程错误，报给用户而不是重试
//	bad_request  → 客户端 bug，重试无意义
//
// 若这些码在 Agent 侧装错，Hub 会做出完全相反的决定——例如把
// "归档已被关闭"当成"重新导出就好了"，于是每次重试都重新导出 2 GiB。
// 因此这里逐个断言码值，而不是只断言"有错误"。

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"arkade/agent/internal/imagetransfer"
	"arkade/agent/internal/runtime"
	"arkade/protocol/envelope"
)

// archiveTestRuntime 是能导出的假运行时。
//
// ============================ 为什么四个方法都要显式实现 ============================
// runtime.ImageTransferRuntime 是**完整方法集**断言（PullImage / SaveImage /
// LoadImage / InspectImage 四个），少一个 NewManager 的类型断言就会失败，
// 于是整条路径静默退化成"引擎不支持镜像传输"。
//
// 内嵌 *fakeRuntime 或 runtime.Runtime 都不解决问题：
//   - 前者不实现传输方法；
//   - 后者只补上主接口，传输方法仍要靠这里显式写。
//
// 因此这里四个全写，其余主接口方法由内嵌的 fakeRuntime 提供。
type archiveTestRuntime struct {
	*fakeRuntime
	payload []byte
}

func (r *archiveTestRuntime) PullImage(context.Context, string, string, string,
	func(runtime.PullProgress)) (runtime.PullResult, error) {
	return runtime.PullResult{}, nil
}

func (r *archiveTestRuntime) SaveImage(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(r.payload)), nil
}

func (r *archiveTestRuntime) LoadImage(_ context.Context, body io.Reader, size int64) ([]runtime.Image, error) {
	// 必须把 body 读完：否则"读归档"的路径没有被验证过。
	n, _ := io.Copy(io.Discard, body)
	if n != size {
		return nil, errors.New("读到的字节数与声明不符")
	}
	return nil, nil
}

func (r *archiveTestRuntime) InspectImage(context.Context, string) (runtime.ImageDetail, error) {
	return runtime.ImageDetail{ID: "sha256:test", Size: int64(len(r.payload))}, nil
}

// newArchiveHandlerRegistry 构造一个只注册归档方法的注册表。
func newArchiveHandlerRegistry(t *testing.T, payload []byte) *Registry {
	t.Helper()
	mgr, err := imagetransfer.NewManager(t.TempDir(),
		&archiveTestRuntime{fakeRuntime: &fakeRuntime{}, payload: payload},
		imagetransfer.DefaultLimits())
	if err != nil {
		t.Fatalf("构造传输管理器失败: %v", err)
	}
	if !mgr.Supported() {
		t.Fatal("假运行时必须被识别为支持镜像传输，否则测不到导出路径")
	}
	reg := NewRegistry()
	reg.Register(envelope.MethodImageArchiveExport, newImageArchiveExportHandler(mgr))
	reg.Register(envelope.MethodImageArchiveOpen, newImageArchiveOpenHandler(mgr))
	reg.Register(envelope.MethodImageArchiveChunkGet, newImageArchiveChunkGetHandler(mgr))
	reg.Register(envelope.MethodImageArchiveClose, newImageArchiveCloseHandler(mgr))
	return reg
}

// callArchive 发一个请求并返回响应信封。
func callArchive(t *testing.T, reg *Registry, method string, payload any) *envelope.Envelope {
	t.Helper()
	req, err := envelope.NewRequest("req-archive", method, "agent:test", payload)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	return reg.Dispatch(context.Background(), req)
}

// decodeArchive 把响应载荷解码成目标结构。
func decodeArchive(t *testing.T, resp *envelope.Envelope, dst any) {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("期望成功，实际错误 %s: %s", resp.Error.Code, resp.Error.Message)
	}
	if err := json.Unmarshal(resp.Payload, dst); err != nil {
		t.Fatalf("解码响应失败: %v（原始 %s）", err, string(resp.Payload))
	}
}

// exportOnce 导出一份归档并返回结果。
func exportOnce(t *testing.T, reg *Registry, ref string) envelope.ImageArchiveExportResult {
	t.Helper()
	resp := callArchive(t, reg, envelope.MethodImageArchiveExport,
		envelope.ImageArchiveExportPayload{Reference: ref, Hold: true})
	var out envelope.ImageArchiveExportResult
	decodeArchive(t, resp, &out)
	return out
}

// TestArchiveExportReturnsIdentity 验证导出返回完整的身份信息。
//
// Hub 必须同时拿到 export_id、大小与校验和：少了任何一个，
// 后续复制都无法做完整性校验（那是 task-14 的核心防线）。
func TestArchiveExportReturnsIdentity(t *testing.T) {
	payload := []byte("archive-bytes-for-handler")
	reg := newArchiveHandlerRegistry(t, payload)

	got := exportOnce(t, reg, "nginx:1.25")
	if got.ExportID == "" {
		t.Fatal("必须返回 export_id")
	}
	if len(got.ExportID) != exportIDLen {
		t.Fatalf("export_id 应为 %d 位，实际 %d 位（%q）", exportIDLen, len(got.ExportID), got.ExportID)
	}
	if got.SizeBytes != int64(len(payload)) {
		t.Fatalf("大小应为 %d，实际 %d", len(payload), got.SizeBytes)
	}
	if len(got.SHA256) != 64 {
		t.Fatalf("sha256 应为 64 位十六进制，实际 %q", got.SHA256)
	}
	if got.ExpiresAt == "" {
		t.Fatal("必须返回过期时间（调用方要据此规划迁移）")
	}
}

// TestArchiveChunkGetRoundTrip 验证经 handler 分块读取能还原原字节。
//
// 这是端到端的读路径验证：协议 → handler 校验 → imagetransfer → storage。
// 只测 imagetransfer 不够，因为 handler 还要做 base64 与 offset 的转换。
func TestArchiveChunkGetRoundTrip(t *testing.T) {
	payload := []byte(strings.Repeat("payload-block-", 40000)) // > 256 KiB
	reg := newArchiveHandlerRegistry(t, payload)
	exp := exportOnce(t, reg, "redis:7")

	var assembled []byte
	var offset int64
	for {
		resp := callArchive(t, reg, envelope.MethodImageArchiveChunkGet,
			envelope.ImageArchiveChunkGetPayload{ExportID: exp.ExportID, Offset: offset, MaxBytes: 256 << 10})
		var chunk envelope.ImageChunkGetResult
		decodeArchive(t, resp, &chunk)
		if chunk.Offset != offset {
			t.Fatalf("回显 offset 不匹配：%d vs %d", chunk.Offset, offset)
		}
		if chunk.DataBase64 == "" {
			break
		}
		raw, derr := base64.StdEncoding.DecodeString(chunk.DataBase64)
		if derr != nil {
			t.Fatalf("分块不是合法 base64: %v", derr)
		}
		assembled = append(assembled, raw...)
		offset += int64(len(raw))
		if chunk.EOF {
			break
		}
	}
	if string(assembled) != string(payload) {
		t.Fatalf("拼回的字节与原归档不一致（%d vs %d 字节）", len(assembled), len(payload))
	}
}

// TestArchiveOpenUnknownIsNotFound 验证未知 ExportID 返回 not_found。
func TestArchiveOpenUnknownIsNotFound(t *testing.T) {
	reg := newArchiveHandlerRegistry(t, []byte("x"))
	resp := callArchive(t, reg, envelope.MethodImageArchiveOpen,
		envelope.ImageArchiveOpenPayload{ExportID: strings.Repeat("0", 32)})
	if resp.Error == nil {
		t.Fatal("未知归档应返回错误")
	}
	if resp.Error.Code != envelope.CodeNotFound {
		t.Fatalf("未知归档应返回 not_found，实际 %s（%s）", resp.Error.Code, resp.Error.Message)
	}
}

// TestArchiveOpenMalformedIDIsBadRequest 验证非法 ExportID 返回 bad_request。
//
// ============================ 这条是安全断言 ============================
// ExportID 会被拼进文件系统路径。若形态非法的输入能走到那一层，
// 就等于把路径拼装交给了下游的善意——这里必须**在进入任何路径之前**拒绝。
func TestArchiveOpenMalformedIDIsBadRequest(t *testing.T) {
	reg := newArchiveHandlerRegistry(t, []byte("x"))
	for _, bad := range []string{
		"",
		"short",
		"../etc/passwd",
		strings.Repeat("A", 32), // 大写：不合法
		strings.Repeat("g", 32), // 非十六进制
		strings.Repeat("a", 33), // 过长
	} {
		resp := callArchive(t, reg, envelope.MethodImageArchiveOpen,
			envelope.ImageArchiveOpenPayload{ExportID: bad})
		if resp.Error == nil {
			t.Fatalf("非法的 export_id %q 应被拒绝", bad)
		}
		if resp.Error.Code != envelope.CodeBadRequest {
			t.Fatalf("非法的 export_id %q 应返回 bad_request，实际 %s", bad, resp.Error.Code)
		}
	}
}

// TestArchiveAfterCloseIsNotFound 验证关闭后读取返回 not_found。
//
// Hub 侧对此的处理是"重新导出"，因此这里必须是 not_found 而不是 conflict：
// 对 Hub 而言"已关闭"与"已过期"的下一步动作完全相同。
func TestArchiveAfterCloseIsNotFound(t *testing.T) {
	reg := newArchiveHandlerRegistry(t, []byte("closing-archive"))
	exp := exportOnce(t, reg, "alpine:3.20")

	resp := callArchive(t, reg, envelope.MethodImageArchiveClose,
		envelope.ImageArchiveClosePayload{ExportID: exp.ExportID})
	var closed envelope.ImageArchiveCloseResult
	decodeArchive(t, resp, &closed)
	if !closed.Released {
		t.Fatalf("首次关闭应真的释放，实际 released=false（%s）", closed.Message)
	}

	resp = callArchive(t, reg, envelope.MethodImageArchiveOpen,
		envelope.ImageArchiveOpenPayload{ExportID: exp.ExportID})
	if resp.Error == nil || resp.Error.Code != envelope.CodeNotFound {
		t.Fatalf("关闭后 open 应返回 not_found，实际 %v", resp.Error)
	}
	resp = callArchive(t, reg, envelope.MethodImageArchiveChunkGet,
		envelope.ImageArchiveChunkGetPayload{ExportID: exp.ExportID, Offset: 0, MaxBytes: 1024})
	if resp.Error == nil || resp.Error.Code != envelope.CodeNotFound {
		t.Fatalf("关闭后读块应返回 not_found，实际 %v", resp.Error)
	}
}

// TestArchiveDoubleCloseIsSuccess 验证重复关闭不是错误。
//
// 关闭一个已经不存在的归档，调用方要的结果（不再占盘）已达成。
// 报错只会迫使上层写一段"吞掉错误"的代码，反而掩盖真正的问题。
func TestArchiveDoubleCloseIsSuccess(t *testing.T) {
	reg := newArchiveHandlerRegistry(t, []byte("twice"))
	exp := exportOnce(t, reg, "busybox")

	var first envelope.ImageArchiveCloseResult
	decodeArchive(t, callArchive(t, reg, envelope.MethodImageArchiveClose,
		envelope.ImageArchiveClosePayload{ExportID: exp.ExportID}), &first)

	resp := callArchive(t, reg, envelope.MethodImageArchiveClose,
		envelope.ImageArchiveClosePayload{ExportID: exp.ExportID})
	var second envelope.ImageArchiveCloseResult
	decodeArchive(t, resp, &second)
	if second.Released {
		t.Fatal("重复关闭不应声称释放了东西")
	}
	if !strings.Contains(second.Message, "不存在") {
		t.Fatalf("应说明归档不存在，实际 %q", second.Message)
	}
}

// TestArchiveNegativeOffsetIsBadRequest 验证负 offset 被拒绝。
func TestArchiveNegativeOffsetIsBadRequest(t *testing.T) {
	reg := newArchiveHandlerRegistry(t, []byte("offset-check"))
	exp := exportOnce(t, reg, "nginx")
	resp := callArchive(t, reg, envelope.MethodImageArchiveChunkGet,
		envelope.ImageArchiveChunkGetPayload{ExportID: exp.ExportID, Offset: -1, MaxBytes: 1024})
	if resp.Error == nil || resp.Error.Code != envelope.CodeBadRequest {
		t.Fatalf("负 offset 应返回 bad_request，实际 %v", resp.Error)
	}
}

// TestArchiveRejectsInvalidReference 验证非法镜像引用被拒绝（沿用既有校验）。
func TestArchiveRejectsInvalidReference(t *testing.T) {
	reg := newArchiveHandlerRegistry(t, []byte("x"))
	for _, bad := range []string{"", "http://evil/x", "user:pass@host/x"} {
		resp := callArchive(t, reg, envelope.MethodImageArchiveExport,
			envelope.ImageArchiveExportPayload{Reference: bad})
		if resp.Error == nil {
			t.Fatalf("非法引用 %q 应被拒绝", bad)
		}
	}
}
