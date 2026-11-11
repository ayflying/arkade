package stackmigrate

import (
	"context"
	"fmt"
	"sync"
)

func (m *Manager) planVolumes(ctx context.Context, t *Task, o Options) (VolumePlan, error) {
	plan, err := m.backend.VolumesOf(ctx, o.Source, o.StackName)
	if err != nil {
		if o.IncludeVolumes {
			return VolumePlan{}, fmt.Errorf("解析源端数据卷清单失败：%w", err)
		}
		t.appendEvent(StreamStderr, "无法解析该编排的数据卷清单（不影响本次迁移，但无法告知跳过了几个卷）")
		return VolumePlan{}, nil
	}
	plan.Volumes = unique(plan.Volumes)
	if o.IncludeVolumes {
		if len(plan.Volumes) > 0 {
			t.appendEvent(StreamStdout, "需要迁移的数据卷 "+itoa(len(plan.Volumes))+" 个："+joinComma(plan.Volumes))
		} else {
			t.appendEvent(StreamStdout, "该编排没有需要迁移的命名数据卷")
		}
		if len(plan.External) > 0 {
			t.appendEvent(StreamStdout, "其中外部卷 "+itoa(len(plan.External))+" 个（迁移的是副本，不保证与其它使用者一致）："+joinComma(plan.External))
		}
	} else if len(plan.Volumes) > 0 {
		t.appendEvent(StreamStdout, "本次未迁移数据卷，目标上将是空数据（该项目有 "+itoa(len(plan.Volumes))+" 个数据卷被跳过）")
	} else {
		t.appendEvent(StreamStdout, "本次未迁移数据卷（该项目没有命名数据卷）")
	}
	if plan.BindDirs > 0 {
		t.appendEvent(StreamStdout, "已跳过宿主机绑定目录 "+itoa(plan.BindDirs)+" 个（迁移不会复制宿主机上的目录内容）")
	}
	if plan.Anonymous > 0 && o.IncludeVolumes {
		t.appendEvent(StreamStdout, "已跳过匿名卷 "+itoa(plan.Anonymous)+" 个（没有稳定名字，无法迁移）")
	}
	return plan, nil
}

// Progress callbacks may arrive concurrently. Gate both image and volume events
// into four monotonic buckets to bound event volume and eliminate callback races.
func progress(t *Task, kind, name string) func(int64, int64) {
	var mu sync.Mutex
	last := 0
	return func(done, total int64) {
		if total <= 0 || done <= 0 {
			return
		}
		pct := int(float64(done) / float64(total) * 100)
		if pct > 100 {
			pct = 100
		}
		mu.Lock()
		defer mu.Unlock()
		bucket := pct / 25
		if bucket <= last {
			return
		}
		last = bucket
		t.appendEvent(StreamStdout, "  "+kind+" "+name+" 已传输 "+itoa(pct)+"%")
	}
}
