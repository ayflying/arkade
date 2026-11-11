package stackmigration

import (
	"context"
	"strings"
	"time"

	"arkade/hub/internal/imagetask"
	"arkade/hub/internal/stackmigrate"
	"github.com/gogf/gf/v2/errors/gerror"
)

type chunkSink struct{ put func(int64, []byte) error }

func (s chunkSink) Write(offset int64, data []byte) error { return s.put(offset, data) }

func (b *Backend) CopyImage(ctx context.Context, source, target, reference string, progress func(int64, int64)) error {
	reader, err := imagetask.NewArchiveReader(imageArchive{b.deps.Images})
	if err != nil {
		return err
	}
	raw, err := b.deps.Images.ExportArchive(ctx, source, reference)
	ref := archiveRef(raw)
	// Export may allocate before returning an invalid descriptor; always close a known ID.
	if ref.ExportID != "" {
		defer func() {
			clean, cancel := cleanupContext()
			defer cancel()
			_, _, _ = reader.Close(clean, source, ref.ExportID)
		}()
	}
	if err != nil {
		return gerror.New("源镜像导出失败")
	}
	if ref.ExportID == "" || ref.Reference != reference || ref.SizeBytes <= 0 || ref.SizeBytes > imagetask.MaxArchiveSize || !validDigest(ref.SHA256) {
		return gerror.New("源镜像归档描述无效")
	}
	load, err := b.deps.Images.BeginLoad(ctx, target, ref.SizeBytes, ref.SHA256)
	if load.ID != "" {
		defer func() {
			clean, cancel := cleanupContext()
			defer cancel()
			_ = b.deps.Images.Cancel(clean, target, load.ID)
			_ = b.deps.Images.Release(clean, target, load.ID)
		}()
	}
	if err != nil || load.ID == "" {
		return gerror.New("目标镜像接收失败")
	}
	sink := chunkSink{put: func(offset int64, data []byte) error {
		return b.deps.Images.PutChunk(ctx, target, load.ID, offset, data)
	}}
	if err = reader.Copy(ctx, source, ref, sink, progress); err != nil {
		return gerror.New("镜像归档传输或完整性校验失败")
	}
	task, err := b.deps.Images.CommitLoad(ctx, target, load.ID, ref.SHA256)
	if err == nil {
		err = b.waitImage(ctx, target, load.ID, task)
	}
	if err != nil {
		return &stackmigrate.ResidualError{Err: gerror.New("目标镜像导入未确认完成，可能留下部分镜像"), Residual: stackmigrate.Residual{Uncertain: true}}
	}
	return nil
}
func (b *Backend) waitImage(ctx context.Context, agent, id string, t imagetask.RemoteTask) error {
	for {
		if t.ID != "" && t.ID != id {
			return gerror.New("镜像任务身份不匹配")
		}
		switch t.Status {
		case "succeeded":
			return nil
		case "failed", "canceled":
			return gerror.New("镜像导入失败")
		case "pending", "running", "queued", "receiving":
		default:
			return gerror.New("未知镜像任务状态")
		}
		if err := wait(ctx, b.poll); err != nil {
			return err
		}
		var err error
		t, err = b.deps.Images.Status(ctx, agent, id)
		if err != nil {
			return err
		}
	}
}
func cleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}
func wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func validDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range strings.ToLower(s) {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
