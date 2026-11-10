package helperfs

// compose_source.go：hidden CLI `compose-source-helper` 的主体。
//
// 与 volume-file-helper 的差异：
//   - 根固定 /source（工作目录的只读 bind），不接受参数；
//   - 只做一件事：按**相对路径**读一个普通文件并回传 base64；
//   - 不列目录、不写、不删、不 rename。
//
// 因此它比 volume helper 的能力面窄得多：即使被误用，
// 也只能读到 /source 之内的一份普通文件。

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
)

const (
	// SourceRoot 是工作目录在 helper 容器内的固定挂载点。
	SourceRoot = "/source"
	// SourceRequestPath 直接复用 RequestPath：**注入端只有这一个位置**。
	//
	// 真实故障：这里曾写成独立的 /compose-source-request.json，
	// 而公共的 injectHelper 永远往 /request.json 写，
	// 于是 hidden CLI 读不到请求、直接 exit 3、不写任何回执，
	// 上层只能看到"进程失败且未留下结果"，最终全部 runtime_failed。
	// 用同一个常量而不是各自写字面量，从根上消除这类两边漂移。
	SourceRequestPath = RequestPath
	// SourceResponsePath 是结果文件位置（必须与 runtime 侧读取路径一致）。
	SourceResponsePath = ResponsePath
	// MaxSourceBytes 是单份 compose 文件上限（对齐 ScanImportSafety）。
	MaxSourceBytes = 1 << 20
)

// SourceRequest 是线上请求。
type SourceRequest struct {
	// ConfigFile 是相对于 /source 的路径。
	ConfigFile string `json:"config_file"`
}

// SourceResponse 是线上结果。
type SourceResponse struct {
	ContentBase64 string `json:"content_base64,omitempty"`
	// Error 是固定中文原因，**不含任何路径**。
	Error string `json:"error,omitempty"`
}

// RunComposeSource 是 hidden CLI 命令主体，返回进程退出码。
func RunComposeSource() int {
	raw, err := os.ReadFile(SourceRequestPath)
	if err != nil {
		return 3
	}
	var req SourceRequest
	if uerr := json.Unmarshal(raw, &req); uerr != nil {
		writeSourceResponse(SourceResponse{Error: "请求格式不正确"})
		return 2
	}

	resp := readSourceFile(req)
	writeSourceResponse(resp)
	if resp.Error != "" {
		return 2
	}
	return 0
}

// readSourceFile 在 /source 内安全读取配置。
func readSourceFile(req SourceRequest) SourceResponse {
	rel, perr := cleanPath(req.ConfigFile)
	if perr != nil || rel == "" {
		return SourceResponse{Error: "配置文件路径不合法"}
	}

	root, err := os.OpenRoot(SourceRoot)
	if err != nil {
		return SourceResponse{Error: "无法打开工作目录"}
	}
	defer func() { _ = root.Close() }()

	// 复用 volume 路径的安全打开：逐级 openat(O_NOFOLLOW) + O_NONBLOCK，
	// 同 fd fstat 保证读的对象就是校验的对象。
	f, info, oerr := safeOpenFile(root, rel, os.O_RDONLY)
	if oerr != nil {
		return SourceResponse{Error: sourceErrText(oerr)}
	}
	defer func() { _ = f.Close() }()

	if info.Size() > MaxSourceBytes {
		return SourceResponse{Error: "编排文件超过 1MB 上限"}
	}
	// 多读 1 字节判定超限，避免 LimitReader 静默截断成半份 YAML。
	data, rerr := io.ReadAll(io.LimitReader(f, MaxSourceBytes+1))
	if rerr != nil {
		return SourceResponse{Error: "读取编排文件失败"}
	}
	if len(data) > MaxSourceBytes {
		return SourceResponse{Error: "编排文件超过 1MB 上限"}
	}
	return SourceResponse{ContentBase64: base64.StdEncoding.EncodeToString(data)}
}

// sourceErrText 把内部错误映射成固定中文（不含路径）。
func sourceErrText(err error) string {
	switch {
	case errors.Is(err, ErrSymlink):
		return "配置文件是符号链接，拒绝读取"
	case errors.Is(err, ErrHardlink):
		return "配置文件存在多个硬链接，拒绝读取"
	case errors.Is(err, ErrNotRegular):
		return "配置文件不是普通文件，拒绝读取"
	case errors.Is(err, os.ErrNotExist):
		return "配置文件不存在"
	// 权限必须单独成一条：help 容器以 root 运行但 CapDrop ALL，
	// 没有 DAC_OVERRIDE，因此 0600/0700 的卷或文件**会**读失败。
	// 归到"读取配置文件失败"会让用户以为是我们坏了，
	// 而实际原因是他该调整文件权限（我们绝不擅自 chmod/chown）。
	case errors.Is(err, os.ErrPermission):
		return "权限不足，无法读取配置文件"
	}
	return "读取配置文件失败"
}

// writeSourceResponse 写结果文件（尽力而为）。
func writeSourceResponse(resp SourceResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_ = os.WriteFile(SourceResponsePath, data, 0o600)
}
