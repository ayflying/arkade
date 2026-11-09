package handler

// image_transfer.go 是镜像 拉取/导出/导入 的方法处理器。
//
// ============================ 分层 ============================
//
// handler 只做四件事：解码 → 校验引用 → 调 imagetransfer.Manager → 转协议结构。
// 所有状态机、配额、幂等、TTL 都在 imagetransfer 包里（便于单独测试，
// 也避免两处规则打架）。
//
// ============================ 关键边界 ============================
//
//  1. **不依赖 composeMgr**：镜像传输与编排无关。Agent 没有数据目录做编排时，
//     镜像传输仍应可用（只要 data_dir 存在即可建 transfers 目录）。
//  2. **错误文案由本层造句**，绝不把引擎/registry 原文回给 Hub：
//     原文常含 registry 认证失败详情、完整 URL 与内网地址。
//  3. **取消不可回滚**：load 方向的取消可能已写入部分镜像层。
//     这是镜像存储没有事务造成的，必须原样告知，不能宣称"已回滚"。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"arkade/agent/internal/imagetransfer"
	"arkade/protocol/envelope"
)

// maxImageRefLen 是镜像引用的长度上限。
//
// 镜像引用最长也就两三百字节（含 registry + 路径 + digest），
// 给 512 已经很宽松。设上限是为了让明显的垃圾/攻击输入在解码后立刻被拒，
// 而不是被拼进 URL 再交给引擎。
const maxImageRefLen = 512

// newImagePullHandler 处理 image.pull。
func newImagePullHandler(mgr *imagetransfer.Manager) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.ImagePullPayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		ref, err := validateImageRef(p.Reference)
		if err != nil {
			return nil, err
		}
		// platform / digest 首批不暴露 Web，但协议保留了字段：
		// 给了就必须合法，**绝不静默忽略**——静默忽略一个用户以为生效的
		// 完整性约束，比直接报错危险得多。
		if err := validatePlatform(p.Platform); err != nil {
			return nil, err
		}
		if err := validateDigest(p.Digest); err != nil {
			return nil, err
		}

		snap, serr := mgr.Pull(ctx, ref, p.Platform, p.Digest)
		if serr != nil {
			return nil, transferError(serr)
		}
		return toImageTask(snap), nil
	}
}

// newImageSaveHandler 处理 image.save。
func newImageSaveHandler(mgr *imagetransfer.Manager) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.ImageSavePayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		ref, err := validateImageRef(p.Reference)
		if err != nil {
			return nil, err
		}
		snap, serr := mgr.Save(ctx, ref)
		if serr != nil {
			return nil, transferError(serr)
		}
		return toImageTask(snap), nil
	}
}

// newImageLoadBeginHandler 处理 image.load.begin。
func newImageLoadBeginHandler(mgr *imagetransfer.Manager) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.ImageLoadBeginPayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		if p.SizeBytes <= 0 {
			return nil, badRequestf("必须提供归档的确切字节数（size_bytes > 0），否则无法在上传前判断磁盘配额")
		}
		sha := strings.TrimSpace(p.SHA256)
		if sha != "" && !isHexSHA(sha) {
			return nil, badRequestf("sha256 格式不正确（应为 64 位十六进制）")
		}
		snap, serr := mgr.BeginLoad(ctx, p.SizeBytes, sha)
		if serr != nil {
			return nil, transferError(serr)
		}
		return toImageTask(snap), nil
	}
}

// newImageChunkPutHandler 处理 image.chunk.put。
func newImageChunkPutHandler(mgr *imagetransfer.Manager) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.ImageChunkPutPayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.TaskID) == "" {
			return nil, badRequestf("task_id 不能为空")
		}
		if p.Offset < 0 {
			return nil, badRequestf("offset 不能为负")
		}
		data, derr := base64.StdEncoding.DecodeString(p.DataBase64)
		if derr != nil {
			return nil, badRequestf("data_base64 不是合法的 base64")
		}

		received, dup, perr := mgr.PutChunk(ctx, p.TaskID, p.Offset, data)
		if perr != nil {
			return nil, transferError(perr)
		}
		return envelope.ImageChunkPutResult{
			TaskID:        p.TaskID,
			ReceivedBytes: received,
			Duplicate:     dup,
		}, nil
	}
}

// newImageLoadCommitHandler 处理 image.load.commit。
func newImageLoadCommitHandler(mgr *imagetransfer.Manager) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.ImageLoadCommitPayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.TaskID) == "" {
			return nil, badRequestf("task_id 不能为空")
		}
		sha := strings.TrimSpace(p.SHA256)
		if sha != "" && !isHexSHA(sha) {
			return nil, badRequestf("sha256 格式不正确（应为 64 位十六进制）")
		}
		snap, serr := mgr.CommitLoad(ctx, p.TaskID, sha)
		if serr != nil {
			return nil, transferError(serr)
		}
		// 提交是异步的：这里返回的是"已受理"的任务快照，
		// 真正的 load 结果要靠 image.task.status 轮询。
		return toImageTask(snap), nil
	}
}

// newImageChunkGetHandler 处理 image.chunk.get。
func newImageChunkGetHandler(mgr *imagetransfer.Manager) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.ImageChunkGetPayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.TaskID) == "" {
			return nil, badRequestf("task_id 不能为空")
		}
		if p.Offset < 0 {
			return nil, badRequestf("offset 不能为负")
		}
		data, offset, eof, size, gerr := mgr.GetChunk(ctx, p.TaskID, p.Offset, p.MaxBytes)
		if gerr != nil {
			return nil, transferError(gerr)
		}
		return envelope.ImageChunkGetResult{
			TaskID:     p.TaskID,
			DataBase64: data,
			Offset:     offset,
			EOF:        eof,
			SizeBytes:  size,
		}, nil
	}
}

// newImageTaskStatusHandler 处理 image.task.status。
func newImageTaskStatusHandler(mgr *imagetransfer.Manager) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		id, err := decodeTaskID(payload)
		if err != nil {
			return nil, err
		}
		snap, serr := mgr.Status(id)
		if serr != nil {
			return nil, transferError(serr)
		}
		return toImageTask(snap), nil
	}
}

// newImageTaskCancelHandler 处理 image.task.cancel。
func newImageTaskCancelHandler(mgr *imagetransfer.Manager) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		id, err := decodeTaskID(payload)
		if err != nil {
			return nil, err
		}
		canceled, msg, cerr := mgr.Cancel(id)
		if cerr != nil {
			return nil, transferError(cerr)
		}
		return envelope.ImageTaskCancelResult{
			TaskID: id, Canceled: canceled, Message: msg,
		}, nil
	}
}

// newImageTaskReleaseHandler 处理 image.task.release。
func newImageTaskReleaseHandler(mgr *imagetransfer.Manager) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		id, err := decodeTaskID(payload)
		if err != nil {
			return nil, err
		}
		released, msg, rerr := mgr.Release(id)
		if rerr != nil {
			return nil, transferError(rerr)
		}
		return envelope.ImageTaskReleaseResult{
			TaskID: id, Released: released, Message: msg,
		}, nil
	}
}

// decodeTaskID 解出 task_id。
func decodeTaskID(payload []byte) (string, error) {
	var p envelope.ImageTaskPayload
	if err := decodePayloadInto(payload, &p); err != nil {
		return "", err
	}
	id := strings.TrimSpace(p.TaskID)
	if id == "" {
		return "", badRequestf("task_id 不能为空")
	}
	return id, nil
}

// toImageTask 把内部快照转成协议结构。
//
// 两处刻意的转换：
//   - BytesTotal == 0 → 协议里写 null（**未知**不能用 0 冒充，
//     否则界面会显示"0 / 0 字节"的假进度）；
//   - Progress 由字节数推算，无法推算时写 null 而不是 0。
func toImageTask(s imagetransfer.Snapshot) envelope.ImageTask {
	out := envelope.ImageTask{
		TaskID:    s.ID,
		Status:    string(s.Status),
		Operation: string(s.Operation),
		Reference: s.Reference,
		Phase:     s.Phase,
		Message:   s.Message,
		BytesDone: s.BytesDone,
		SHA256:    s.SHA256,
		SizeBytes: s.SizeBytes,
	}
	if s.BytesTotal > 0 {
		total := s.BytesTotal
		out.BytesTotal = &total
		p := float64(s.BytesDone) / float64(total)
		if p < 0 {
			p = 0
		}
		if p > 1 {
			p = 1
		}
		out.Progress = &p
	}
	return out
}

// transferError 把 imagetransfer 的领域错误翻成带中文说明的 handler 错误。
//
// 统一在这里翻译的理由：领域错误是英文常量，直接回给 Hub 会让界面
// 出现一串英文；而在每个 handler 里各写一遍翻译，必然出现
// "同一件事两种说法"。集中一处，文案只有一份。
func transferError(err error) error {
	switch {
	case errors.Is(err, imagetransfer.ErrTaskNotFound):
		return badRequestf("传输任务不存在（可能已过期或被释放）")
	case errors.Is(err, imagetransfer.ErrTooManyTasks):
		return badRequestf("并发传输任务已达上限，请等待当前任务结束")
	case errors.Is(err, imagetransfer.ErrArchiveTooLarge):
		return badRequestf("归档超过单次传输上限（当前限制 %d MiB）",
			imagetransfer.DefaultMaxArchiveBytes>>20)
	case errors.Is(err, imagetransfer.ErrQuotaExceeded):
		return badRequestf("传输临时区可用配额不足（总量上限 %d MiB），请先释放已有任务",
			imagetransfer.DefaultMaxTotalArchiveBytes>>20)
	case errors.Is(err, imagetransfer.ErrChunkTooLarge):
		return badRequestf("单个分块超过上限（%d KiB）", imagetransfer.ChunkSize>>10)
	case errors.Is(err, imagetransfer.ErrBadOffset):
		return badRequestf("分块偏移不正确：请按已接收字节数继续上传（中间不能缺块）")
	case errors.Is(err, imagetransfer.ErrSizeMismatch):
		return badRequestf("上传的字节数超出了声明的归档大小")
	case errors.Is(err, imagetransfer.ErrMissingChecksum):
		return badRequestf("缺少归档校验和：begin 与 commit 至少需要提供一次 sha256")
	case errors.Is(err, imagetransfer.ErrChecksumMismatch):
		return badRequestf("归档校验和不一致，已拒绝导入（文件可能在传输中损坏）")
	case errors.Is(err, imagetransfer.ErrTaskState):
		return badRequestf("当前任务状态不允许该操作")
	}
	return err
}

// validateImageRef 校验镜像引用。
//
// ============================ 校验的重点 ============================
//
// 拒绝的是"**不是一个镜像引用**"的东西，而不是随意的字符白名单：
//
//  1. 控制字符与空白：会被 shell/HTTP 层解释，属于注入载体；
//  2. URL 用户名密码（`user:pass@host/...`）：说明调用方在试图传凭据，
//     而首批明确不收凭据——放行只会让凭据悄悄进入日志；
//  3. 协议前缀（`http://`、`https://`）：镜像引用不含 scheme，
//     带着它说明调用方误解了语义，静默接受会让后续请求拼出畸形 URL；
//  4. `..` 路径段：镜像名里不存在，出现即是构造攻击。
//
// 通过校验后仍然会用 url.PathEscape/QueryEscape 正确转义（见 engine）——
// 校验与转义是两道各自独立的防线，不能因为有了校验就省掉转义。
func validateImageRef(ref string) (string, error) {
	r := strings.TrimSpace(ref)
	if r == "" {
		return "", badRequestf("镜像引用不能为空")
	}
	if len(r) > maxImageRefLen {
		return "", badRequestf("镜像引用过长（上限 %d 字节）", maxImageRefLen)
	}
	for _, c := range r {
		if c < 0x20 || c == 0x7f || c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			return "", badRequestf("镜像引用包含非法字符（控制字符或空白）")
		}
	}
	lower := strings.ToLower(r)
	for _, scheme := range []string{"http://", "https://", "tcp://", "unix://", "ssh://"} {
		if strings.HasPrefix(lower, scheme) {
			return "", badRequestf("镜像引用不应包含协议前缀（请直接写仓库/镜像名:标签）")
		}
	}
	// URL 用户名密码形态：在第一个 "/" 之前出现 "@"。
	firstSlash := strings.Index(r, "/")
	firstAt := strings.Index(r, "@")
	if firstAt >= 0 && (firstSlash < 0 || firstAt < firstSlash) {
		return "", badRequestf("镜像引用不应包含用户名或密码（本功能仅支持公共仓库）")
	}
	// 路径段里不允许 ".."，也不允许连续斜杠造成的空段。
	for _, seg := range strings.Split(r, "/") {
		if seg == "" {
			return "", badRequestf("镜像引用包含空的路径段")
		}
		if seg == ".." || seg == "." {
			return "", badRequestf("镜像引用包含非法的路径段（. 或 ..）")
		}
	}
	return r, nil
}

// validatePlatform 校验 platform（os/arch[/variant]）。
func validatePlatform(platform string) error {
	p := strings.TrimSpace(platform)
	if p == "" {
		return nil
	}
	parts := strings.Split(p, "/")
	if len(parts) < 2 || len(parts) > 3 {
		return badRequestf("platform 格式不正确（应为 os/arch 或 os/arch/variant）")
	}
	for _, part := range parts {
		if part == "" {
			return badRequestf("platform 格式不正确（存在空段）")
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '.' && c != '-' && c != '_' {
				return badRequestf("platform 含非法字符")
			}
		}
	}
	return nil
}

// validateDigest 校验 digest（sha256:64hex）。
func validateDigest(digest string) error {
	d := strings.TrimSpace(digest)
	if d == "" {
		return nil
	}
	hexPart := d
	if strings.HasPrefix(d, "sha256:") {
		hexPart = strings.TrimPrefix(d, "sha256:")
	}
	if !isHexSHA(hexPart) {
		return badRequestf("digest 格式不正确（应为 sha256:64 位十六进制）")
	}
	return nil
}

// isHexSHA 校验 64 位十六进制。
func isHexSHA(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') && !(c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// 确保 json 包被使用（decodePayloadInto 在 compose_adopt.go 里定义，
// 这里保留一个编译期使用点，避免将来误删导入）。
var _ = json.Marshal
