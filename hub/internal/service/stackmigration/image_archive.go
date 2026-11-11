package stackmigration

import (
	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/imagetask"
	"context"
)

// imageArchive is a field-only conversion between transport and reader contracts.
type imageArchive struct{ ImageBackend }

func archiveRef(ref agentlink.ArchiveRef) imagetask.ArchiveRef {
	return imagetask.ArchiveRef{ExportID: ref.ExportID, Reference: ref.Reference, SizeBytes: ref.SizeBytes, SHA256: ref.SHA256, ExpiresAt: ref.ExpiresAt}
}
func (a imageArchive) ExportArchive(ctx context.Context, agent, ref string) (imagetask.ArchiveRef, error) {
	out, err := a.ImageBackend.ExportArchive(ctx, agent, ref)
	return archiveRef(out), err
}
func (a imageArchive) OpenArchive(ctx context.Context, agent, id string) (imagetask.ArchiveRef, error) {
	out, err := a.ImageBackend.OpenArchive(ctx, agent, id)
	return archiveRef(out), err
}
