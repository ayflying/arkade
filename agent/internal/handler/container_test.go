package handler

// container_test.go：容器列表摘要的字段映射。
//
// 重点是 StackID —— 它把容器关联到 compose 项目，
// Hub 的外部项目详情页依赖这个字段做归组。若这里不填，
// 详情页只能拿到一堆互不相干的容器（用户看到的"项目详情是空的"）。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"arkade/agent/internal/runtime"
	"arkade/protocol/envelope"
)

// TestContainerSummaryStackIDFromComposeLabel 验证有 compose 项目标签时填进 StackID。
func TestContainerSummaryStackIDFromComposeLabel(t *testing.T) {
	rt := &fakeRuntime{containers: []runtime.Container{
		{
			ID:     "aaaaaaaaaaaaaaaaaaaa",
			Name:   "demo-web-1",
			Image:  "nginx:alpine",
			State:  "running",
			Labels: map[string]string{"com.docker.compose.project": "demo"},
		},
	}}
	h := newListHandler(rt)
	res, err := h(context.Background(), nil)
	if err != nil {
		t.Fatalf("不应失败: %v", err)
	}
	list, ok := res.(envelope.ContainerListResult)
	if !ok {
		t.Fatalf("类型不符: %T", res)
	}
	if len(list.Containers) != 1 {
		t.Fatalf("应返回 1 个容器，实际 %d", len(list.Containers))
	}
	if list.Containers[0].StackID != "demo" {
		t.Fatalf("StackID 应为 compose 项目名 demo，实际 %q", list.Containers[0].StackID)
	}
}

// TestContainerSummaryStackIDEmptyWithoutLabel 验证手工 docker run 的容器不带该标签时为空。
func TestContainerSummaryStackIDEmptyWithoutLabel(t *testing.T) {
	rt := &fakeRuntime{containers: []runtime.Container{
		{
			ID:     "bbbbbbbbbbbbbbbbbbbb",
			Name:   "manual",
			State:  "running",
			Labels: map[string]string{"other": "x"},
		},
		{
			// Labels 为 nil 也要安全（手工容器常见形态）。
			ID:    "cccccccccccccccccccc",
			Name:  "no-labels",
			State: "exited",
		},
	}}
	h := newListHandler(rt)
	res, err := h(context.Background(), nil)
	if err != nil {
		t.Fatalf("不应失败: %v", err)
	}
	list := res.(envelope.ContainerListResult)
	for _, c := range list.Containers {
		if c.StackID != "" {
			t.Fatalf("无 compose 标签时 StackID 必须为空，%s 得到 %q", c.Name, c.StackID)
		}
	}
}

// TestContainerSummaryDoesNotLeakAllLabels 验证**不**把全部标签搬进协议。
//
// 标签里可能有用户自定义的敏感信息，协议只暴露关联所需的项目名。
func TestContainerSummaryDoesNotLeakAllLabels(t *testing.T) {
	rt := &fakeRuntime{containers: []runtime.Container{
		{
			ID:    "dddddddddddddddddddd",
			Name:  "demo-db-1",
			State: "running",
			Labels: map[string]string{
				"com.docker.compose.project": "demo",
				"com.docker.compose.service": "db",
				"secret.token":               "SHOULD-NOT-APPEAR",
			},
		},
	}}
	h := newListHandler(rt)
	res, _ := h(context.Background(), nil)
	c := res.(envelope.ContainerListResult).Containers[0]

	// ContainerSummary 里根本没有 Labels 字段，这里用整体序列化反向确认。
	rawBytes, jerr := json.Marshal(c)
	if jerr != nil {
		t.Fatal(jerr)
	}
	raw := string(rawBytes)
	if strings.Contains(raw, "SHOULD-NOT-APPEAR") {
		t.Fatalf("协议载荷不应包含自定义标签值: %s", raw)
	}
	// 服务名标签也不应出现（只带项目名）。
	if strings.Contains(raw, `"db"`) {
		t.Fatalf("不应暴露服务名标签: %s", raw)
	}
}
