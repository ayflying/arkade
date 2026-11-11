package handler

import (
	"arkade/agent/internal/compose"
	"arkade/agent/internal/compose/migrate"
	"arkade/protocol/envelope"
	"context"
)

func importComposition(ctx context.Context, importer migrate.CompositionImporter, req migrate.ComposeArchive) (migrate.ComposeImportResult, error) {
	if real, ok := importer.(composeWorkspaceImporter); ok {
		return real.mgr.ImportMigration(ctx, req)
	}
	return migrate.ImportComposition(importer, req)
}
func newComposeMigrationPlanHandler(mgr *compose.Manager) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.ComposeMigrationPlanPayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		plan, err := mgr.MigrationPlan(ctx, p.Name)
		if err != nil {
			return nil, toEnvelopeError(err)
		}
		out := envelope.ComposeMigrationPlanResult{Name: plan.Name, Config: plan.Config, Env: plan.Env, ConfigHash: plan.ConfigHash, Images: plan.Images, Busy: plan.Busy,
			Volumes: []envelope.ComposeMigrationVolume{}, BindMounts: []envelope.ComposeMigrationSkippedMount{}, AnonymousMounts: []envelope.ComposeMigrationSkippedMount{}}
		for _, v := range plan.Volumes.Volumes {
			kind := "named"
			if v.Kind == migrate.VolumeExternal {
				kind = "external"
			} else if v.Kind == migrate.VolumeExplicit {
				kind = "explicit"
			}
			out.Volumes = append(out.Volumes, envelope.ComposeMigrationVolume{Key: v.Key, Name: v.Name, Kind: kind, Services: v.Services})
		}
		for _, v := range plan.Volumes.BindMounts {
			out.BindMounts = append(out.BindMounts, envelope.ComposeMigrationSkippedMount{Service: v.Service, Source: v.Source, Target: v.Target, Reason: v.Reason})
		}
		for _, v := range plan.Volumes.AnonymousMounts {
			out.AnonymousMounts = append(out.AnonymousMounts, envelope.ComposeMigrationSkippedMount{Service: v.Service, Source: v.Source, Target: v.Target, Reason: v.Reason})
		}
		return out, nil
	}
}
func newVolumeImportCancelHandler(imp *migrate.Importer) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.VolumeImportCancelPayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		snap, err := imp.Cancel(ctx, p.TaskID)
		if err != nil {
			return nil, migrateError(err)
		}
		return envelope.VolumeImportResult{TaskID: snap.TaskID, Volume: snap.Volume, Status: snap.Status, Phase: snap.Phase, Message: snap.Message, BytesDone: snap.Received, SHA256: snap.SHA256}, nil
	}
}
