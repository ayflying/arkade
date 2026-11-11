package agentlink

import (
	"context"
	"encoding/base64"
	"strings"

	"arkade/protocol/envelope"
)

// MaxVolumeArchiveSizeHard matches Agent migrate.MaxVolumeArchiveBytes; image
// archives retain their separate 2 GiB bound. Neither is a memory allocation size.
const MaxVolumeArchiveSizeHard int64 = 128 << 30

func transferInvalid(message string) *ConnError {
	return &ConnError{Code: envelope.CodeBadRequest, Message: message}
}
func transferMalformed(message string) *ConnError {
	return &ConnError{Code: envelope.CodeUnsupported, Message: message}
}

// migrationCall preserves Registry's exact transport/protocol error, including cancellation.
func (o *Ops) migrationCall(ctx context.Context, agent, method string, payload, out any) *ConnError {
	raw, err := o.reg.Call(ctx, agent, method, payload)
	if err != nil {
		return err
	}
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return transferMalformed("Agent 未返回迁移操作结果")
	}
	return decodeInto(raw, out)
}

func validateVolumeArchive(id, volume string, size int64, hash string) *ConnError {
	if id == "" || volume == "" || size <= 0 || size > MaxVolumeArchiveSizeHard || !isHexSHA256(hash) {
		return transferMalformed("Agent 返回的卷归档信息无效")
	}
	return nil
}

func (o *Ops) VolumeArchiveExport(ctx context.Context, agent string, req envelope.VolumeArchiveExportPayload) (envelope.VolumeArchiveExportResult, *ConnError) {
	var out envelope.VolumeArchiveExportResult
	if err := o.migrationCall(ctx, agent, envelope.MethodVolumeArchiveExport, req, &out); err != nil {
		return out, err
	}
	if err := validateVolumeArchive(out.ExportID, out.Volume, out.SizeBytes, out.SHA256); err != nil {
		return out, err
	}
	if out.Volume != req.Volume {
		return out, transferMalformed("Agent 返回的卷归档名称不一致")
	}
	return out, nil
}
func (o *Ops) VolumeArchiveOpen(ctx context.Context, agent, id string) (envelope.VolumeArchiveOpenResult, *ConnError) {
	var out envelope.VolumeArchiveOpenResult
	if err := o.migrationCall(ctx, agent, envelope.MethodVolumeArchiveOpen, envelope.VolumeArchiveOpenPayload{ExportID: id}, &out); err != nil {
		return out, err
	}
	if err := validateVolumeArchive(out.ExportID, out.Volume, out.SizeBytes, out.SHA256); err != nil {
		return out, err
	}
	if out.ExportID != id {
		return out, transferMalformed("Agent 返回的卷归档标识不一致")
	}
	return out, nil
}
func (o *Ops) VolumeArchiveClose(ctx context.Context, agent, id string) (envelope.VolumeArchiveCloseResult, *ConnError) {
	var out envelope.VolumeArchiveCloseResult
	if err := o.migrationCall(ctx, agent, envelope.MethodVolumeArchiveClose, envelope.VolumeArchiveClosePayload{ExportID: id}, &out); err != nil {
		return out, err
	}
	if out.ExportID != id {
		return out, transferMalformed("Agent 返回的卷归档标识不一致")
	}
	return out, nil
}

func decodeVolumeChunk(out envelope.ImageChunkGetResult, offset int64, max int) ([]byte, bool, *ConnError) {
	if out.Offset != offset || len(out.DataBase64) > base64.StdEncoding.EncodedLen(max) {
		return nil, false, transferMalformed("Agent 返回的卷分块越界")
	}
	data, err := base64.StdEncoding.DecodeString(out.DataBase64)
	if err != nil || len(data) > max || (len(data) == 0 && !out.EOF) {
		return nil, false, transferMalformed("Agent 返回的卷分块无效")
	}
	return data, out.EOF, nil
}
func (o *Ops) VolumeArchiveChunkGet(ctx context.Context, agent, id string, offset int64, max int) ([]byte, bool, *ConnError) {
	if offset < 0 {
		return nil, false, transferInvalid("分块偏移不能为负")
	}
	if max <= 0 || max > ChunkSizeHardLimit {
		max = ChunkSizeHardLimit
	}
	var out envelope.ImageChunkGetResult
	if err := o.migrationCall(ctx, agent, envelope.MethodVolumeArchiveChunkGet, envelope.VolumeArchiveChunkGetPayload{ExportID: id, Offset: offset, MaxBytes: int64(max)}, &out); err != nil {
		return nil, false, err
	}
	return decodeVolumeChunk(out, offset, max)
}

func (o *Ops) VolumeImportBegin(ctx context.Context, agent string, req envelope.VolumeImportBeginPayload) (envelope.VolumeImportBeginResult, *ConnError) {
	var out envelope.VolumeImportBeginResult
	if req.SizeBytes <= 0 || req.SizeBytes > MaxVolumeArchiveSizeHard || !isHexSHA256(req.SHA256) {
		return out, transferInvalid("卷归档大小或校验和无效")
	}
	if err := o.migrationCall(ctx, agent, envelope.MethodVolumeImportBegin, req, &out); err != nil {
		return out, err
	}
	if out.TaskID == "" || (req.TaskID != "" && out.TaskID != req.TaskID) || out.Volume != req.Volume || out.SizeBytes != req.SizeBytes || !strings.EqualFold(out.SHA256, req.SHA256) || out.ReceivedBytes < 0 || out.ReceivedBytes > req.SizeBytes || (!out.AlreadyReused && out.ReceivedBytes != 0) {
		return out, transferMalformed("Agent 返回的卷导入确认信息不一致")
	}
	return out, nil
}
func validateVolumeAck(offset int64, size int, received int64) *ConnError {
	if received != offset+int64(size) {
		return transferMalformed("Agent 未确认精确的卷分块长度")
	}
	return nil
}
func (o *Ops) VolumeImportChunkPut(ctx context.Context, agent, id string, offset int64, data []byte) *ConnError {
	if offset < 0 || offset > MaxVolumeArchiveSizeHard || len(data) == 0 || len(data) > ChunkSizeHardLimit || int64(len(data)) > MaxVolumeArchiveSizeHard-offset {
		return transferInvalid("卷分块偏移或大小无效")
	}
	var out envelope.VolumeImportChunkPutResult
	if err := o.migrationCall(ctx, agent, envelope.MethodVolumeImportChunkPut, envelope.VolumeImportChunkPutPayload{TaskID: id, Offset: offset, DataBase64: base64.StdEncoding.EncodeToString(data)}, &out); err != nil {
		return err
	}
	return validateVolumeAck(offset, len(data), out.ReceivedBytes)
}
func (o *Ops) VolumeImportCommit(ctx context.Context, agent, id, hash string) (envelope.VolumeImportResult, *ConnError) {
	var out envelope.VolumeImportResult
	if err := o.migrationCall(ctx, agent, envelope.MethodVolumeImportCommit, envelope.VolumeImportCommitPayload{TaskID: id, SHA256: hash}, &out); err != nil {
		return out, err
	}
	return out, validateVolumeStatus(out, id)
}
func (o *Ops) VolumeImportStatus(ctx context.Context, agent, id string) (envelope.VolumeImportResult, *ConnError) {
	var out envelope.VolumeImportResult
	if err := o.migrationCall(ctx, agent, envelope.MethodVolumeImportStatus, envelope.VolumeImportStatusPayload{TaskID: id}, &out); err != nil {
		return out, err
	}
	return out, validateVolumeStatus(out, id)
}
func (o *Ops) VolumeImportCancel(ctx context.Context, agent, id string) *ConnError {
	var out envelope.VolumeImportResult
	if err := o.migrationCall(ctx, agent, envelope.MethodVolumeImportCancel, envelope.VolumeImportCancelPayload{TaskID: id}, &out); err != nil {
		return err
	}
	if err := validateVolumeStatus(out, id); err != nil {
		return err
	}
	if out.Status != envelope.VolumeImportCanceled {
		return transferMalformed("Agent 未确认卷导入已取消")
	}
	return nil
}
func validateVolumeStatus(out envelope.VolumeImportResult, id string) *ConnError {
	if out.TaskID != id || out.Volume == "" || out.BytesDone < 0 {
		return transferMalformed("Agent 返回的卷导入任务信息无效")
	}
	switch out.Status {
	case envelope.VolumeImportPending, envelope.VolumeImportRunning, envelope.VolumeImportSucceeded, envelope.VolumeImportFailed, envelope.VolumeImportCanceled:
		return nil
	}
	return transferMalformed("Agent 返回的卷导入状态无效")
}
