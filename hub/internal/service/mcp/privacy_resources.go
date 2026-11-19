package mcp

// resourcePublicFields lists JSON leaf paths from the resource and image-task
// response DTOs. Paths traverse arrays implicitly; no object is passed through
// wholesale. A nil result means the tool has no policy in this group.
func resourcePublicFields(tool string) []string {
	switch tool {
	case "arkade_list_containers":
		return []string{
			"agent_id", "containers.id", "containers.name", "containers.image",
			"containers.state", "containers.origin", "containers.stack_id",
			"containers.created", "containers.networks.name",
			"containers.published_ports.host_port", "containers.published_ports.container_port", "containers.published_ports.protocol",
		}
	case "arkade_inspect_container":
		return []string{
			"container.id", "container.name", "container.image", "container.state",
			"container.created", "container.started_at", "container.finished_at", "container.exit_code", "container.restart_count",
			"container.networks.name", "container.mounts.type", "container.mounts.read_only",
			"container.published_ports.host_port", "container.published_ports.container_port", "container.published_ports.protocol",
			"container.exposed_ports.[]", "container.resource_limits.memory_bytes", "container.resource_limits.nano_cpus",
			"container.resource_limits.pids_limit",
		}
	case "arkade_list_container_metrics":
		return []string{
			"agent_id", "stale_after_seconds", "containers.container_id", "containers.at",
			"containers.stale", "containers.stats_available", "containers.cpu_percent", "containers.memory_bytes",
			"containers.memory_limit_bytes", "containers.memory_percent", "containers.network_available",
			"containers.network_rx_bytes", "containers.network_tx_bytes", "containers.rates_available",
			"containers.network_rx_per_second", "containers.network_tx_per_second",
		}
	case "arkade_start_container", "arkade_stop_container", "arkade_restart_container", "arkade_delete_container":
		return []string{"container_id", "state"}
	case "arkade_list_networks":
		return []string{
			"agent_id", "networks.id", "networks.name", "networks.driver", "networks.scope",
			"networks.internal", "networks.containers", "networks.created",
		}
	case "arkade_list_volumes":
		return []string{"agent_id", "volumes.name", "volumes.driver", "volumes.created", "volumes.in_use"}
	case "arkade_list_volume_files":
		return []string{
			"result.entries.name", "result.entries.kind", "result.entries.size", "result.entries.modified_at", "result.entries.mode",
		}
	case "arkade_read_volume_file", "arkade_create_volume_file", "arkade_replace_volume_file":
		return []string{"result.sha256", "result.size"}
	case "arkade_create_volume_directory", "arkade_rename_volume_file", "arkade_delete_volume_file":
		return []string{}
	case "arkade_list_images":
		return []string{"agent_id", "images.id", "images.tags.[]", "images.size", "images.created", "images.containers", "images.dangling"}
	case "arkade_delete_image", "arkade_delete_network", "arkade_delete_volume":
		return []string{} // HTTP 204: known tool with no response fields.
	case "arkade_list_image_tasks":
		return imageTaskPublicFields("tasks.")
	case "arkade_get_image_task", "arkade_cancel_image_task", "arkade_pull_image", "arkade_export_image", "arkade_migrate_image":
		return imageTaskPublicFields("task.")
	default:
		return nil
	}
}

func imageTaskPublicFields(prefix string) []string {
	return []string{
		prefix + "id", prefix + "kind", prefix + "agent_id", prefix + "target_agent_id",
		prefix + "status", prefix + "phase", prefix + "progress", prefix + "bytes_done", prefix + "bytes_total",
		prefix + "created_at", prefix + "finished_at", prefix + "download_ready",
	}
}
