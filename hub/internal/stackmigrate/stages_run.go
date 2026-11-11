package stackmigrate

import (
	"context"
	"fmt"
	"strings"
)

func stage(t *Task, s string) {
	t.setStage(s)
	t.appendEvent(StreamStage, "第 "+itoa(StageIndex(s))+" 步「"+StageTitle(s)+"」")
}
func (m *Manager) runStages(ctx context.Context, t *Task, o Options, res *Residual) error {
	stage(t, StageValidate)
	if err := m.guard(ctx, o, nil, false); err != nil {
		return err
	}
	t.appendEvent(StreamStdout, "校验通过：两端在线、权限具备、目标无同名编排")
	stage(t, StageReadConfig)
	// Preserve the original first-read failure stage while rechecking permissions.
	if err := m.permissions(ctx, o); err != nil {
		return err
	}
	config, env, hash, err := m.backend.ReadCompose(ctx, o.Source, o.StackName)
	if err != nil {
		return fmt.Errorf("读取源端编排配置失败：%w", err)
	}
	if strings.TrimSpace(config) == "" {
		return fmt.Errorf("源端编排配置为空，无法迁移")
	}
	source := &sourceConfig{config: config, env: env, hash: hash}
	t.appendEvent(StreamStdout, "已读取源端编排配置（哈希 "+hash+"）")
	stage(t, StagePlan)
	if err := m.guard(ctx, o, source, false); err != nil {
		return err
	}
	images, err := m.backend.ImagesOf(ctx, o.Source, o.StackName)
	if err != nil {
		return fmt.Errorf("解析源端镜像清单失败：%w", err)
	}
	images = unique(images)
	t.appendEvent(StreamStdout, "需要迁移的容器镜像 "+itoa(len(images))+" 个")
	plan, err := m.planVolumes(ctx, t, o)
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.imageCount = len(images)
	t.volumeCount = len(plan.Volumes)
	t.bindDirs = plan.BindDirs
	if !o.IncludeVolumes {
		t.skippedVolumes = len(plan.Volumes)
	}
	t.mu.Unlock()
	stage(t, StageImages)
	if err := m.guard(ctx, o, source, false); err != nil {
		return err
	}
	if len(images) == 0 {
		t.appendEvent(StreamStdout, "该编排没有需要迁移的镜像")
	}
	for i, ref := range images {
		if err := m.guard(ctx, o, source, false); err != nil {
			return err
		}
		t.appendEvent(StreamStdout, "["+itoa(i+1)+"/"+itoa(len(images))+"] 迁移镜像 "+ref)
		err := m.backend.CopyImage(ctx, o.Source, o.Target, ref, progress(t, "镜像", ref))
		if err != nil {
			mergeFailure(res, err)
			return fmt.Errorf("迁移镜像 %s 失败：%w", ref, err)
		}
		res.ImagesImported++
		t.setResidual(*res)
	}
	if len(images) > 0 {
		t.appendEvent(StreamStdout, "镜像迁移完成，共 "+itoa(len(images))+" 个")
	}
	if o.IncludeVolumes && len(plan.Volumes) > 0 {
		stage(t, StageVolumes)
		for i, vol := range plan.Volumes {
			if err := m.guard(ctx, o, source, false); err != nil {
				return err
			}
			t.appendEvent(StreamStdout, "["+itoa(i+1)+"/"+itoa(len(plan.Volumes))+"] 迁移数据卷 "+vol)
			err := m.backend.CopyVolume(ctx, o.Source, o.Target, vol, progress(t, "数据卷", vol))
			if err != nil {
				mergeFailure(res, err)
				return fmt.Errorf("迁移数据卷 %s 失败：%w", vol, err)
			}
			res.VolumesRestored++
			t.setResidual(*res)
		}
		t.appendEvent(StreamStdout, "数据卷迁移完成，共 "+itoa(len(plan.Volumes))+" 个")
	}
	stage(t, StageImportConf)
	if err := m.guard(ctx, o, source, false); err != nil {
		return err
	}
	if err := m.backend.ImportCompose(ctx, o.Target, o.StackName, config, env, hash); err != nil {
		mergeFailure(res, err)
		return fmt.Errorf("写入目标端编排配置失败：%w", err)
	}
	res.ComposeWritten = true
	t.setResidual(*res)
	t.appendEvent(StreamStdout, "已在目标服务器写入编排配置")
	stage(t, StageStart)
	if err := m.guard(ctx, o, source, true); err != nil {
		return err
	}
	if !o.StartAfter {
		t.appendEvent(StreamStdout, "已迁移，未启动（按你的选择没有在目标服务器上启动该编排）")
		return nil
	}
	// Before starting, require target content identity too: a target editor must
	// never cause migration to start somebody else's changed configuration.
	targetConfig, targetEnv, targetHash, err := m.backend.ReadCompose(ctx, o.Target, o.StackName)
	if err != nil {
		return fmt.Errorf("启动前无法确认目标端配置：%w", err)
	}
	if targetConfig != config || targetEnv != env || targetHash != hash {
		return fmt.Errorf("目标端编排配置已变化，未启动；请检查目标端配置")
	}
	if err := m.backend.StartStack(ctx, o.Target, o.StackName); err != nil {
		mergeFailure(res, err)
		return fmt.Errorf("在目标服务器启动编排失败：%w", err)
	}
	res.Started = true
	t.setResidual(*res)
	t.appendEvent(StreamStdout, "已在目标服务器启动该编排")
	return nil
}
func unique(items []string) []string {
	out := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		if !seen[item] {
			seen[item] = true
			out = append(out, item)
		}
	}
	return out
}
