package compose

import (
	"context"
	"encoding/json"
	"strings"
)

// Compose ps 只提供容器标识及镜像引用；引用可变，不能代表容器真正使用的内容。
func (m *Manager) inspectDeployImages(ctx context.Context, project string, entries []psEntry) {
	for i := range entries {
		e := &entries[i]
		if e.ID == "" {
			continue
		}
		res := m.probeDocker(ctx, project, TimeoutShort, "container", "inspect", "--format", "{{.Image}}", "--", e.ID)
		if res.ExitCode != 0 {
			continue
		}
		e.ImageID = deployImageID(res.Stdout)
		if e.ImageID != "" {
			e.RepoDigests = m.deployImageDigests(ctx, project, e.ImageID)
		}
	}
}

// 比较完整 ID，不把短 ID、空输出或诊断文本当成变化的证据。
func deployImageID(raw string) string {
	id := strings.TrimSpace(raw)
	hex, ok := strings.CutPrefix(id, "sha256:")
	if !ok || len(hex) != 64 || strings.ContainsFunc(hex, func(r rune) bool {
		return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f')
	}) {
		return ""
	}
	return id
}

func (m *Manager) localImageID(ctx context.Context, project, ref string) string {
	res := m.probeDocker(ctx, project, TimeoutShort, "image", "inspect", "--format", "{{.Id}}", "--", ref)
	if res.ExitCode != 0 {
		return ""
	}
	return deployImageID(res.Stdout)
}

func (m *Manager) deployImageDigests(ctx context.Context, project, id string) []string {
	res := m.probeDocker(ctx, project, TimeoutShort, "image", "inspect", "--format", "{{json .RepoDigests}}", "--", id)
	var digests []string
	if res.ExitCode == 0 {
		_ = json.Unmarshal([]byte(res.Stdout), &digests)
	}
	return digests
}

// 任一已核实的副本使用了不同的镜像，才允许替换这个服务。
// 没有容器时保持原样，「更新部署」不会因此创建或启动服务。
func deployServiceChange(entries []psEntry, service, target string) (changed, verified bool) {
	missing := false
	for _, e := range entries {
		if e.Service != service {
			continue
		}
		if e.ImageID == "" || target == "" {
			missing = true
			continue
		}
		if e.ImageID != target {
			changed = true
		}
	}
	return changed, !missing
}
