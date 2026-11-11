package handler

import (
	"arkade/agent/internal/compose/migrate"
	"arkade/protocol/envelope"
	"context"
	"encoding/base64"
	"errors"
	"strings"
)

// newVolumeImportBeginHandler 处理 volume.import.begin。
func newVolumeImportBeginHandler(imp *migrate.Importer) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.VolumeImportBeginPayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		snap, reused, err := imp.Begin(ctx, strings.TrimSpace(p.Volume), p.Project,
			p.SizeBytes, strings.TrimSpace(p.SHA256), strings.TrimSpace(p.TaskID))
		if err != nil {
			return nil, migrateError(err)
		}
		return envelope.VolumeImportBeginResult{
			TaskID: snap.TaskID, Volume: snap.Volume,
			SizeBytes: snap.BytesTotal, SHA256: p.SHA256,
			ReceivedBytes: snap.Received, AlreadyReused: reused,
		}, nil
	}
}

// newVolumeImportChunkPutHandler 处理 volume.import.chunk.put。
func newVolumeImportChunkPutHandler(imp *migrate.Importer) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.VolumeImportChunkPutPayload
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
		if len(data) > migrate.ChunkSize {
			return nil, badRequestf("单个分块超过上限（%d 字节）", migrate.ChunkSize)
		}
		received, perr := imp.PutChunk(ctx, p.TaskID, p.Offset, data)
		if perr != nil {
			return nil, migrateError(perr)
		}
		return envelope.VolumeImportChunkPutResult{ReceivedBytes: received}, nil
	}
}

// newVolumeImportCommitHandler 处理 volume.import.commit。
func newVolumeImportCommitHandler(imp *migrate.Importer) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.VolumeImportCommitPayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.TaskID) == "" {
			return nil, badRequestf("task_id 不能为空")
		}
		snap, err := imp.Commit(ctx, p.TaskID, strings.TrimSpace(p.SHA256))
		if err != nil {
			return nil, migrateError(err)
		}
		return envelope.VolumeImportResult{
			TaskID: snap.TaskID, Volume: snap.Volume, Status: snap.Status,
			Phase: snap.Phase, Message: snap.Message,
			BytesDone: snap.Received, SHA256: snap.SHA256,
		}, nil
	}
}

// newVolumeImportStatusHandler 处理 volume.import.status。
func newVolumeImportStatusHandler(imp *migrate.Importer) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.VolumeImportStatusPayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		snap, err := imp.Status(strings.TrimSpace(p.TaskID))
		if err != nil {
			return nil, migrateError(err)
		}
		return envelope.VolumeImportResult{
			TaskID: snap.TaskID, Volume: snap.Volume, Status: snap.Status,
			Phase: snap.Phase, Message: snap.Message,
			BytesDone: snap.Received, SHA256: snap.SHA256,
		}, nil
	}
}

// newComposeImportHandler 处理 compose.import。
func newComposeImportHandler(importer migrate.CompositionImporter) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.ComposeImportPayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		res, err := importComposition(ctx, importer, migrate.ComposeArchive{
			Name: p.Name, Config: p.Config, Env: p.Env,
			ExpectHash: p.ExpectHash, Overwrite: false,
		})
		if err != nil {
			if errors.Is(err, migrate.ErrComposeExists) {
				// 同名冲突是**用户可见的产品行为**（提示改名），
				// 因此用 conflict 而不是内部错误。
				return nil, envelope.NewError(envelope.CodeConflict,
					"目标服务器上已存在同名编排，请先改名后再迁移")
			}
			return nil, migrateError(err)
		}
		return envelope.ComposeImportResult{
			Name: res.Name, ConfigHash: res.ConfigHash, AlreadyExists: res.AlreadyExists,
		}, nil
	}
}
