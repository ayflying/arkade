package helperfs

// 容器内入口：读 /request.json，在 /data 上执行，写 /response.json。
//
// 为什么用文件而不是参数/环境变量传请求：
//   - 请求里有 base64 内容（最多 4MiB），放命令行会撞 ARG_MAX 且出现在 ps 里；
//   - 文件方式让注入路径与控制路径彻底分离（见 runtime 侧的 PUT archive）。

import (
	"encoding/json"
	"errors"
	"os"
)

const (
	// RequestPath 是容器内的请求文件位置。
	RequestPath = "/request.json"
	// ResponsePath 是容器内的响应文件位置。
	ResponsePath = "/response.json"
	// DataRoot 是卷在 helper 容器里的挂载点。
	DataRoot = "/data"
)

// WireRequest 是请求文件的线上格式（与协议 payload 同名）。
type WireRequest struct {
	Operation      string `json:"operation"`
	Path           string `json:"path"`
	Destination    string `json:"destination,omitempty"`
	DataBase64     string `json:"data_base64,omitempty"`
	ExpectedSHA256 string `json:"expected_sha256,omitempty"`
	CreateOnly     bool   `json:"create_only,omitempty"`
}

// WireResponse 是响应文件的线上格式。
//
// Error 非空表示失败；Code 给调用方一个可判定的类别，
// Message 是给用户看的中文说明（不含卷内路径以外的敏感信息）。
type WireResponse struct {
	Path       string      `json:"path,omitempty"`
	Entries    []WireEntry `json:"entries,omitempty"`
	DataBase64 string      `json:"data_base64,omitempty"`
	SHA256     string      `json:"sha256,omitempty"`
	Size       int64       `json:"size,omitempty"`
	Message    string      `json:"message,omitempty"`
	Error      string      `json:"error,omitempty"`
	Code       string      `json:"code,omitempty"`
}

// WireEntry 是目录项。
type WireEntry struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Size       int64  `json:"size"`
	ModifiedAt int64  `json:"modified_at"`
	Mode       string `json:"mode"`
}

// Run 是隐藏 CLI 命令的主体。返回进程退出码。
//
// 挂载点**固定**为 DataRoot（/data），不接受外部参数：
// 这个隐藏命令只能处理"挂在 /data 上的那个卷"，
// 若允许传入任意路径，它就退化成一个"读容器内任意目录"的工具
// ——虽然仍在容器命名空间内，但没有任何理由给它这种能力。
// 真正的路径选择发生在 runtime 侧（把哪个卷 bind 到 /data）。
//
// 退出码约定：0 成功；2 请求/路径类错误；3 其它错误。
// 无论哪种都会写出响应文件——否则调用方只能看到"退出码非 0"，
// 无法区分是路径被拒还是引擎出错。
func Run() int {
	// 请求文件由 runtime 通过 PUT archive 注入，大小有界且内容可信
	// （我们自己在 handler 里构造并校验过），因此可以直接 ReadFile。
	raw, err := os.ReadFile(RequestPath)
	if err != nil {
		return 3
	}
	var req WireRequest
	if uerr := json.Unmarshal(raw, &req); uerr != nil {
		writeResponse(WireResponse{Error: "请求格式不正确", Code: "bad_request"})
		return 2
	}

	resp := execute(req)
	writeResponse(resp)
	if resp.Error != "" {
		if resp.Code == "bad_request" || resp.Code == "bad_path" {
			return 2
		}
		return 3
	}
	return 0
}

// execute 打开卷根并执行操作。
func execute(req WireRequest) WireResponse {
	// 打开卷根。这里的 os.OpenRoot 是"信任锚"：
	// 后续所有路径操作都被限制在该目录之内。
	root, err := os.OpenRoot(DataRoot)
	if err != nil {
		return WireResponse{Error: "无法打开卷目录", Code: "root_unavailable"}
	}
	defer func() { _ = root.Close() }()

	res, derr := Do(root, Request{
		Operation:      req.Operation,
		Path:           req.Path,
		Destination:    req.Destination,
		DataBase64:     req.DataBase64,
		ExpectedSHA256: req.ExpectedSHA256,
		CreateOnly:     req.CreateOnly,
	})
	if derr != nil {
		return WireResponse{Error: errText(derr), Code: errCode(derr)}
	}

	out := WireResponse{
		Path:       res.Path,
		DataBase64: res.DataBase64,
		SHA256:     res.SHA256,
		Size:       res.Size,
		Message:    res.Message,
	}
	for _, e := range res.Entries {
		out.Entries = append(out.Entries, WireEntry{
			Name: e.Name, Kind: e.Kind, Size: e.Size,
			ModifiedAt: e.ModifiedAt, Mode: e.Mode,
		})
	}
	return out
}

// errText 把错误映射成面向用户的中文说明。
//
// 不回传底层 os 错误原文：其中可能含卷内绝对路径，
// 而调用方只需要知道"为什么被拒"。
func errText(err error) string {
	switch {
	case errors.Is(err, ErrBadPath):
		return "路径不合法（不能是绝对路径或包含 ..、NUL、反斜杠）"
	case errors.Is(err, ErrTooMany):
		return "目录项超过 2000 条，请进入子目录查看"
	case errors.Is(err, ErrFileTooLarge):
		return "文件超过 4 MiB 上限"
	case errors.Is(err, ErrSymlink):
		return "拒绝操作符号链接"
	case errors.Is(err, ErrHardlink):
		return "拒绝操作存在多个硬链接的文件"
	case errors.Is(err, ErrNotEmpty):
		return "目录非空，不递归删除"
	case errors.Is(err, ErrExists):
		return "目标已存在"
	case errors.Is(err, ErrChecksum):
		return "文件已被其他人修改（校验和不匹配），请重新读取后再保存"
	case errors.Is(err, ErrNotRegular):
		return "目标不是普通文件"
	case errors.Is(err, os.ErrNotExist):
		return "路径不存在"
	}
	return "操作失败"
}

// errCode 给出稳定的错误类别，便于调用方与前端分支处理。
func errCode(err error) string {
	switch {
	case errors.Is(err, ErrBadPath):
		return "bad_path"
	case errors.Is(err, ErrTooMany):
		return "too_many"
	case errors.Is(err, ErrFileTooLarge):
		return "too_large"
	case errors.Is(err, ErrSymlink):
		return "symlink"
	case errors.Is(err, ErrHardlink):
		return "hardlink"
	case errors.Is(err, ErrNotEmpty):
		return "not_empty"
	case errors.Is(err, ErrExists):
		return "exists"
	case errors.Is(err, ErrChecksum):
		return "checksum"
	case errors.Is(err, ErrNotRegular):
		return "not_regular"
	case errors.Is(err, os.ErrNotExist):
		return "not_found"
	}
	return "internal"
}

// writeResponse 写响应文件（尽力而为）。
func writeResponse(resp WireResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_ = os.WriteFile(ResponsePath, data, 0o600)
}
