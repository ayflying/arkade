package mcp

// stackPublicFields allowlists success fields from the agent, stack and external
// Compose DTOs. A nil slice means there is no policy for this tool; a non-nil
// empty slice declares a policy exposing no upstream fields.
func stackPublicFields(tool string) []string {
	switch tool {
	case "arkade_list_agents":
		return []string{
			"agents.id", "agents.disabled", "agents.status", "agents.version",
			"agents.runtime", "agents.runtime_version", "agents.last_seen",
		}
	case "arkade_list_stacks":
		fields := stackViewFields([]string{"agent_id", "agent_online"}, "stacks")
		return externalStackViewFields(fields, "external_stacks")
	case "arkade_get_stack":
		return stackViewFields(stackContainerFields(nil), "stack")
	case "arkade_get_stack_config":
		return []string{"version", "content"}
	case "arkade_patch_stack_env":
		return []string{"version", "changed_keys.[]"}
	case "arkade_list_stack_containers":
		return stackContainerFields([]string{"agent_id"})
	case "arkade_create_stack", "arkade_update_stack":
		return stackViewFields([]string{"sync_ok", "result.status"}, "stack")
	case "arkade_delete_stack":
		return []string{"name", "sync_ok", "result.status", "config_hash"}
	case "arkade_deploy_stack", "arkade_update_and_deploy_stack", "arkade_start_stack_operation":
		return stackOperationFields()
	case "arkade_get_stack_operation":
		// The polling endpoint can return either a Compose task or a migration.
		fields := append(stackOperationFields(), "task.agent_id")
		return append(fields, migrationProgressFields()...)
	case "arkade_migrate_stack":
		return append([]string{"task.task_id", "task.agent_id", "task.name", "task.action", "task.status", "task.started_at", "task.finished_at"}, migrationProgressFields()...)
	case "arkade_get_external_stack":
		return externalStackViewFields(stackContainerFields(nil), "project")
	case "arkade_get_external_stack_config":
		return []string{"readable", "config_hash", "draft.source_hash", "draft.version", "draft.updated_at"}
	case "arkade_save_external_stack_draft":
		return []string{"draft.source_hash", "draft.version", "draft.updated_at"}
	case "arkade_retry_external_stack_adopt":
		// RetryAdoptRes has only a human-readable message.
		return []string{}
	case "arkade_preview_stack_import":
		return []string{"name", "can_import", "config_hash"}
	case "arkade_preview_external_stack_adopt":
		return []string{"can_import", "config_hash"}
	case "arkade_import_stack", "arkade_adopt_external_stack":
		return stackViewFields([]string{"sync_ok", "adopted"}, "stack")
	case "arkade_copy_external_stack_config":
		return stackViewFields([]string{"data_included", "started", "source_preserved"}, "stack")
	default:
		return nil
	}
}

func stackViewFields(fields []string, prefix string) []string {
	return append(fields,
		prefix+".agent_id", prefix+".name", prefix+".managed", prefix+".auto_adopt_status",
		prefix+".status", prefix+".config_hash", prefix+".remote_hash", prefix+".drift",
		prefix+".running", prefix+".total", prefix+".version", prefix+".updated_at", prefix+".synced_at",
	)
}

func externalStackViewFields(fields []string, prefix string) []string {
	return append(fields,
		prefix+".name", prefix+".status", prefix+".running", prefix+".total",
		prefix+".managed", prefix+".auto_adopt_status",
	)
}

func stackContainerFields(fields []string) []string {
	// state is the runtime state; status is human-readable text. Omit image
	// references and networking metadata, which can expose host addresses.
	return append(fields,
		"containers.id", "containers.name", "containers.state", "containers.origin",
		"containers.stack_id", "containers.created",
	)
}

func stackOperationFields() []string {
	return []string{
		"task.task_id", "task.name", "task.action", "task.status", "task.started_at", "task.finished_at",
		"task.stack_version", "task.record_removed",
	}
}

func migrationProgressFields() []string {
	return []string{
		"task.migrate.source_agent_id", "task.migrate.target_agent_id",
		"task.migrate.include_volumes", "task.migrate.start_after", "task.migrate.stage",
		"task.migrate.image_count", "task.migrate.volume_count", "task.migrate.skipped_volumes", "task.migrate.bind_dirs",
		"task.migrate.residual.images_imported", "task.migrate.residual.volumes_restored",
		"task.migrate.residual.compose_written", "task.migrate.residual.started", "task.migrate.residual.uncertain",
	}
}
