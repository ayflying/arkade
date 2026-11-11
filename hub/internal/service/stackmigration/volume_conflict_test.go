package stackmigration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/stackmigrate"
	"arkade/protocol/envelope"
)

type conflictVolumeOps struct {
	Ops
	beginConflict                      bool
	errMessage                         string
	commits, restores, closes, cancels int
}

func volumeHash() string { h := sha256.Sum256([]byte("volume")); return hex.EncodeToString(h[:]) }
func (f *conflictVolumeOps) VolumeArchiveExport(context.Context, string, envelope.VolumeArchiveExportPayload) (envelope.VolumeArchiveExportResult, *agentlink.ConnError) {
	return envelope.VolumeArchiveExportResult{ExportID: "export", Volume: "data", SizeBytes: 6, SHA256: volumeHash()}, nil
}
func (f *conflictVolumeOps) VolumeArchiveChunkGet(context.Context, string, string, int64, int) ([]byte, bool, *agentlink.ConnError) {
	return []byte("volume"), true, nil
}
func (f *conflictVolumeOps) VolumeArchiveClose(context.Context, string, string) (envelope.VolumeArchiveCloseResult, *agentlink.ConnError) {
	f.closes++
	return envelope.VolumeArchiveCloseResult{Released: true}, nil
}
func (f *conflictVolumeOps) conflict() *agentlink.ConnError {
	return &agentlink.ConnError{Code: envelope.CodeConflict, Message: f.errMessage}
}
func (f *conflictVolumeOps) VolumeImportBegin(context.Context, string, envelope.VolumeImportBeginPayload) (envelope.VolumeImportBeginResult, *agentlink.ConnError) {
	if f.beginConflict {
		return envelope.VolumeImportBeginResult{}, f.conflict()
	}
	return envelope.VolumeImportBeginResult{TaskID: "task", Volume: "data", SizeBytes: 6, SHA256: volumeHash()}, nil
}
func (f *conflictVolumeOps) VolumeImportChunkPut(context.Context, string, string, int64, []byte) *agentlink.ConnError {
	return nil
}
func (f *conflictVolumeOps) VolumeImportCommit(context.Context, string, string, string) (envelope.VolumeImportResult, *agentlink.ConnError) {
	f.commits++
	return envelope.VolumeImportResult{}, f.conflict()
}
func (f *conflictVolumeOps) VolumeImportStatus(context.Context, string, string) (envelope.VolumeImportResult, *agentlink.ConnError) {
	f.restores++
	panic("rejection must not poll or restore")
}
func (f *conflictVolumeOps) VolumeImportCancel(context.Context, string, string) *agentlink.ConnError {
	f.cancels++
	return nil
}
func TestExistingVolumeRejectionIsConfirmedNoVolumeMutation(t *testing.T) {
	for _, begin := range []bool{true, false} {
		f := &conflictVolumeOps{beginConflict: begin, errMessage: "目标数据卷已存在，已拒绝覆盖"}
		err := New(Deps{Ops: f}).CopyVolume(context.Background(), "source", "target", "data", nil)
		var partial *stackmigrate.ResidualError
		if !errors.As(err, &partial) || partial.Residual.Uncertain || partial.Residual.VolumesRestored != 0 || partial.Residual.ComposeWritten {
			t.Fatalf("wrong residual: %v %+v", err, partial)
		}
		if !strings.Contains(err.Error(), "已拒绝覆盖") {
			t.Fatal(err)
		}
		if f.restores != 0 || f.closes != 1 {
			t.Fatalf("unsafe lifecycle %+v", f)
		}
		if begin && (f.commits != 0 || f.cancels != 0) {
			t.Fatalf("begin conflict continued %+v", f)
		}
		if !begin && (f.commits != 1 || f.cancels != 1) {
			t.Fatalf("commit conflict cleanup %+v", f)
		}
	}
}
func TestUnknownConflictRemainsUncertainAndDoesNotLeakMessage(t *testing.T) {
	f := &conflictVolumeOps{errMessage: "private stderr /secret"}
	err := New(Deps{Ops: f}).CopyVolume(context.Background(), "source", "target", "data", nil)
	var partial *stackmigrate.ResidualError
	if !errors.As(err, &partial) || !partial.Residual.Uncertain || strings.Contains(err.Error(), "private") {
		t.Fatal(err)
	}
}
