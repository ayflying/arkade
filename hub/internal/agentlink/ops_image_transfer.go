package agentlink

import (
	"context"
	"encoding/base64"
	"fmt"

	"arkade/hub/internal/imagetask"
	"arkade/protocol/envelope"
)

// ImageBackend只搬运有界分块；归档不会以整包JSON进入控制通道。
// 协议对象到调度对象的转换集中在此，避免HTTP与Agent各维护一套状态映射。
type ImageBackend struct{ ops *Ops }

func NewImageBackend(ops *Ops) *ImageBackend { return &ImageBackend{ops: ops} }
func (b *ImageBackend) call(ctx context.Context, agent, method string, payload, out any) error {
	raw, err := b.ops.reg.Call(ctx, agent, method, payload)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := decodeInto(raw, out); err != nil {
		return err
	}
	return nil
}
func remoteImageTask(t envelope.ImageTask) imagetask.RemoteTask {
	return imagetask.RemoteTask{ID: t.TaskID, Status: t.Status, Phase: t.Phase, Message: t.Message, Progress: t.Progress, BytesDone: t.BytesDone, BytesTotal: t.BytesTotal, SizeBytes: t.SizeBytes, SHA256: t.SHA256}
}
func (b *ImageBackend) task(ctx context.Context, a, m string, p any) (imagetask.RemoteTask, error) {
	var t envelope.ImageTask
	err := b.call(ctx, a, m, p, &t)
	return remoteImageTask(t), err
}
func (b *ImageBackend) StartPull(ctx context.Context, a, ref string) (imagetask.RemoteTask, error) {
	return b.task(ctx, a, envelope.MethodImagePull, envelope.ImagePullPayload{Reference: ref})
}
func (b *ImageBackend) StartSave(ctx context.Context, a, ref string) (imagetask.RemoteTask, error) {
	return b.task(ctx, a, envelope.MethodImageSave, envelope.ImageSavePayload{Reference: ref})
}
func (b *ImageBackend) BeginLoad(ctx context.Context, a string, size int64, hash string) (imagetask.RemoteTask, error) {
	return b.task(ctx, a, envelope.MethodImageLoadBegin, envelope.ImageLoadBeginPayload{SizeBytes: size, SHA256: hash})
}
func (b *ImageBackend) CommitLoad(ctx context.Context, a, id, hash string) (imagetask.RemoteTask, error) {
	return b.task(ctx, a, envelope.MethodImageLoadCommit, envelope.ImageLoadCommitPayload{TaskID: id, SHA256: hash})
}
func (b *ImageBackend) Status(ctx context.Context, a, id string) (imagetask.RemoteTask, error) {
	return b.task(ctx, a, envelope.MethodImageTaskStatus, envelope.ImageTaskPayload{TaskID: id})
}
func (b *ImageBackend) PutChunk(ctx context.Context, a, id string, offset int64, data []byte) error {
	if len(data) > 256<<10 {
		return fmt.Errorf("传输分块超过上限")
	}
	var out envelope.ImageChunkPutResult
	if err := b.call(ctx, a, envelope.MethodImageChunkPut, envelope.ImageChunkPutPayload{TaskID: id, Offset: offset, DataBase64: base64.StdEncoding.EncodeToString(data)}, &out); err != nil {
		return err
	}
	if out.ReceivedBytes < offset+int64(len(data)) {
		return fmt.Errorf("服务器未确认完整分块")
	}
	return nil
}
func (b *ImageBackend) GetChunk(ctx context.Context, a, id string, offset int64, max int) ([]byte, bool, error) {
	var out envelope.ImageChunkGetResult
	if err := b.call(ctx, a, envelope.MethodImageChunkGet, envelope.ImageChunkGetPayload{TaskID: id, Offset: offset, MaxBytes: int64(max)}, &out); err != nil {
		return nil, false, err
	}
	if out.Offset != offset || len(out.DataBase64) > ((256<<10)+2)/3*4 {
		return nil, false, fmt.Errorf("服务器返回的分块越界")
	}
	data, err := base64.StdEncoding.DecodeString(out.DataBase64)
	if err != nil || len(data) > 256<<10 {
		return nil, false, fmt.Errorf("服务器返回的分块无效")
	}
	return data, out.EOF, nil
}
func (b *ImageBackend) Cancel(ctx context.Context, a, id string) error {
	return b.call(ctx, a, envelope.MethodImageTaskCancel, envelope.ImageTaskPayload{TaskID: id}, nil)
}
func (b *ImageBackend) Release(ctx context.Context, a, id string) error {
	return b.call(ctx, a, envelope.MethodImageTaskRelease, envelope.ImageTaskPayload{TaskID: id}, nil)
}
