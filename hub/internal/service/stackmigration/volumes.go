package stackmigration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"arkade/hub/internal/agentlink"

	"arkade/hub/internal/imagetask"
	"arkade/hub/internal/stackmigrate"
	"arkade/protocol/envelope"
	"github.com/gogf/gf/v2/errors/gerror"
)

// volumeArchive maps only the shared bounded reader protocol, not image operations.
type volumeArchive struct{ ops Ops }

func (v volumeArchive) ExportArchive(ctx context.Context, a, volume string) (imagetask.ArchiveRef, error) {
	out, err := v.ops.VolumeArchiveExport(ctx, a, envelope.VolumeArchiveExportPayload{Volume: volume})
	return imagetask.ArchiveRef{ExportID: out.ExportID, Reference: out.Volume, SizeBytes: out.SizeBytes, SHA256: out.SHA256, ExpiresAt: out.ExpiresAt}, connErr(err)
}
func (v volumeArchive) OpenArchive(context.Context, string, string) (imagetask.ArchiveRef, error) {
	return imagetask.ArchiveRef{}, gerror.New("卷归档不可复用打开")
}
func (v volumeArchive) GetArchiveChunk(ctx context.Context, a, id string, offset int64, max int) ([]byte, bool, error) {
	data, eof, err := v.ops.VolumeArchiveChunkGet(ctx, a, id, offset, max)
	return data, eof, connErr(err)
}
func (v volumeArchive) CloseArchive(ctx context.Context, a, id string) (bool, string, error) {
	out, err := v.ops.VolumeArchiveClose(ctx, a, id)
	return out.Released, out.Message, connErr(err)
}

func (b *Backend) CopyVolume(ctx context.Context, source, target, volume string, progress func(int64, int64)) error {
	archive := volumeArchive{ops: b.deps.Ops}
	ref, err := archive.ExportArchive(ctx, source, volume)
	if ref.ExportID != "" {
		defer func() {
			clean, cancel := cleanupContext()
			defer cancel()
			_, _, _ = archive.CloseArchive(clean, source, ref.ExportID)
		}()
	}
	if err != nil {
		return gerror.New("源数据卷导出失败")
	}
	if ref.Reference != volume || ref.SizeBytes <= 0 || ref.SizeBytes > agentlink.MaxVolumeArchiveSizeHard || !validDigest(ref.SHA256) {
		return gerror.New("源数据卷归档描述无效")
	}
	load, cerr := b.deps.Ops.VolumeImportBegin(ctx, target, envelope.VolumeImportBeginPayload{Volume: volume, SizeBytes: ref.SizeBytes, SHA256: ref.SHA256})
	if load.TaskID != "" {
		defer func() {
			clean, cancel := cleanupContext()
			defer cancel()
			_ = b.deps.Ops.VolumeImportCancel(clean, target, load.TaskID)
		}()
	}
	if cerr != nil {
		if refusal := volumeConflict(cerr); refusal != nil {
			return refusal
		}
		return gerror.New("目标数据卷接收失败")
	}
	if load.TaskID == "" || load.Volume != volume || load.SizeBytes != ref.SizeBytes || !strings.EqualFold(load.SHA256, ref.SHA256) || load.ReceivedBytes != 0 || load.AlreadyReused {
		return gerror.New("目标数据卷任务描述无效")
	}
	if err = copyVolumeChunks(ctx, archive, source, ref, func(offset int64, data []byte) error {
		return connErr(b.deps.Ops.VolumeImportChunkPut(ctx, target, load.TaskID, offset, data))
	}, progress); err != nil {
		return gerror.New("数据卷归档传输或完整性校验失败")
	}
	result, cerr := b.deps.Ops.VolumeImportCommit(ctx, target, load.TaskID, ref.SHA256)
	if cerr != nil {
		if refusal := volumeConflict(cerr); refusal != nil {
			return refusal
		}
		return uncertain("目标数据卷提交未确认，可能留下部分卷数据")
	}
	if err = b.waitVolume(ctx, target, load.TaskID, volume, ref.SHA256, result); err != nil {
		return &stackmigrate.ResidualError{Err: gerror.New("目标数据卷恢复未确认完成，可能留下部分卷数据"), Residual: stackmigrate.Residual{Uncertain: true}}
	}
	return nil
}

// This exact fixed protocol error is emitted before any restore helper starts.
// Other conflict errors must not be interpreted as proof of zero side effects.
func volumeConflict(err *agentlink.ConnError) error {
	if err.Code != envelope.CodeConflict || err.Message != "目标数据卷已存在，已拒绝覆盖" {
		return nil
	}
	return &stackmigrate.ResidualError{Err: gerror.New("目标数据卷已存在或冲突，已拒绝覆盖"), Residual: stackmigrate.Residual{}}
}

func copyVolumeChunks(ctx context.Context, archive volumeArchive, source string, ref imagetask.ArchiveRef, put func(int64, []byte) error, progress func(int64, int64)) error {
	h := sha256.New()
	var offset int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, eof, err := archive.GetArchiveChunk(ctx, source, ref.ExportID, offset, imagetask.ChunkSize)
		if err != nil {
			return err
		}
		if len(data) > imagetask.ChunkSize || int64(len(data)) > ref.SizeBytes-offset || len(data) == 0 && !eof {
			return gerror.New("卷归档分块越界")
		}
		if len(data) > 0 {
			_, _ = h.Write(data)
			if err := put(offset, data); err != nil {
				return err
			}
			offset += int64(len(data))
			if progress != nil {
				progress(offset, ref.SizeBytes)
			}
		}
		if eof {
			break
		}
	}
	if offset != ref.SizeBytes || !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), ref.SHA256) {
		return gerror.New("卷归档完整性校验失败")
	}
	return nil
}
func (b *Backend) waitVolume(ctx context.Context, agent, id, volume, hash string, t envelope.VolumeImportResult) error {
	for {
		if t.TaskID != id || t.Volume != volume {
			return gerror.New("卷任务身份不匹配")
		}
		switch t.Status {
		case envelope.VolumeImportSucceeded:
			if !strings.EqualFold(t.SHA256, hash) {
				return gerror.New("卷任务校验和不匹配")
			}
			return nil
		case envelope.VolumeImportFailed, envelope.VolumeImportCanceled:
			return gerror.New("卷导入失败")
		case envelope.VolumeImportPending, envelope.VolumeImportRunning:
		default:
			return gerror.New("未知卷任务状态")
		}
		if err := wait(ctx, b.poll); err != nil {
			return err
		}
		out, err := b.deps.Ops.VolumeImportStatus(ctx, agent, id)
		if err != nil {
			return err
		}
		t = out
	}
}
