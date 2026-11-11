// Package stackmigration adapts the migration coordinator to stored authority and Agent RPCs.
package stackmigration

import (
	"context"
	"time"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/imagetask"
	"arkade/hub/internal/stackmigrate"
	"arkade/hub/internal/store"
	"arkade/protocol/envelope"
)

type OnlineRegistry interface{ IsOnline(string) bool }
type ImageBackend interface {
	imagetask.Backend
	ExportArchive(context.Context, string, string) (agentlink.ArchiveRef, error)
	OpenArchive(context.Context, string, string) (agentlink.ArchiveRef, error)
	GetArchiveChunk(context.Context, string, string, int64, int) ([]byte, bool, error)
	CloseArchive(context.Context, string, string) (bool, string, error)
}
type Ops interface {
	ComposeList(context.Context, string) (envelope.ComposeListResult, *agentlink.ConnError)
	ComposeMigrationPlan(context.Context, string, envelope.ComposeMigrationPlanPayload) (envelope.ComposeMigrationPlanResult, *agentlink.ConnError)
	ComposeImport(context.Context, string, envelope.ComposeImportPayload) (envelope.ComposeImportResult, *agentlink.ConnError)
	ComposeUp(context.Context, string, envelope.ComposeSpecPayload) (envelope.ComposeOpResult, *agentlink.ConnError)
	VolumeArchiveExport(context.Context, string, envelope.VolumeArchiveExportPayload) (envelope.VolumeArchiveExportResult, *agentlink.ConnError)
	VolumeArchiveChunkGet(context.Context, string, string, int64, int) ([]byte, bool, *agentlink.ConnError)
	VolumeArchiveClose(context.Context, string, string) (envelope.VolumeArchiveCloseResult, *agentlink.ConnError)
	VolumeImportBegin(context.Context, string, envelope.VolumeImportBeginPayload) (envelope.VolumeImportBeginResult, *agentlink.ConnError)
	VolumeImportChunkPut(context.Context, string, string, int64, []byte) *agentlink.ConnError
	VolumeImportCommit(context.Context, string, string, string) (envelope.VolumeImportResult, *agentlink.ConnError)
	VolumeImportStatus(context.Context, string, string) (envelope.VolumeImportResult, *agentlink.ConnError)
	VolumeImportCancel(context.Context, string, string) *agentlink.ConnError
}
type Deps struct {
	DB       *store.DB
	Ops      Ops
	Registry OnlineRegistry
	Images   ImageBackend
}
type Backend struct {
	deps Deps
	poll time.Duration
}

func New(d Deps) *Backend { return &Backend{deps: d, poll: 250 * time.Millisecond} }

var _ stackmigrate.Backend = (*Backend)(nil)
var _ Ops = (*agentlink.Ops)(nil)
var _ ImageBackend = (*agentlink.ImageBackend)(nil)
