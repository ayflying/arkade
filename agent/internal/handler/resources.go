package handler

// resources.go 是镜像 / 网络 / 存储卷类方法的处理器。
//
// 这三个能力都是"代理"性质：Hub 转发请求，Agent 回答本机实况。
// 因此处理器只做「解码 ID → 调运行时 → 转成协议结构」，
// 校验的重点是 ID 非空与路径安全（ID 会拼进 URL）。

import (
	"context"
	"encoding/json"
	"strings"

	"arkade/agent/internal/runtime"
	"arkade/protocol/envelope"
)

// newImageListHandler 处理 image.list。
func newImageListHandler(rt runtime.Runtime) Handler {
	return func(ctx context.Context, _ []byte) (any, error) {
		images, err := rt.ListImages(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]envelope.ImageSummary, 0, len(images))
		for _, img := range images {
			tags := img.Tags
			if tags == nil {
				tags = []string{}
			}
			out = append(out, envelope.ImageSummary{
				ID:         img.ID,
				Tags:       tags,
				Size:       img.Size,
				Created:    img.Created,
				Containers: 0, // 运行时列表接口不返回使用计数，界面如需可另查
				Dangling:   img.Dangling,
			})
		}
		return envelope.ImageListResult{Images: out}, nil
	}
}

// newImageRemoveHandler 处理 image.remove。
func newImageRemoveHandler(rt runtime.Runtime) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.ImageRemovePayload
		if len(payload) == 0 || string(payload) == "null" {
			return nil, badRequestf("缺少载荷：需要 image_id")
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, badRequestf("载荷格式错误: %v", err)
		}
		id := strings.TrimSpace(p.ImageID)
		if id == "" {
			return nil, badRequestf("image_id 不能为空")
		}
		if err := rt.RemoveImage(ctx, id, p.Force); err != nil {
			return nil, err
		}
		return struct{}{}, nil
	}
}

// newNetworkListHandler 处理 network.list。
func newNetworkListHandler(rt runtime.Runtime) Handler {
	return func(ctx context.Context, _ []byte) (any, error) {
		nets, err := rt.ListNetworks(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]envelope.NetworkSummary, 0, len(nets))
		for _, n := range nets {
			labels := n.Labels
			if labels == nil {
				labels = map[string]string{}
			}
			out = append(out, envelope.NetworkSummary{
				ID:         n.ID,
				Name:       n.Name,
				Driver:     n.Driver,
				Scope:      n.Scope,
				Internal:   n.Internal,
				Containers: n.Containers,
				Labels:     labels,
				Created:    n.Created,
			})
		}
		return envelope.NetworkListResult{Networks: out}, nil
	}
}

// newNetworkRemoveHandler 处理 network.remove。
func newNetworkRemoveHandler(rt runtime.Runtime) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.NetworkRemovePayload
		if len(payload) == 0 || string(payload) == "null" {
			return nil, badRequestf("缺少载荷：需要 network_id")
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, badRequestf("载荷格式错误: %v", err)
		}
		id := strings.TrimSpace(p.NetworkID)
		if id == "" {
			return nil, badRequestf("network_id 不能为空")
		}
		if err := rt.RemoveNetwork(ctx, id); err != nil {
			return nil, err
		}
		return struct{}{}, nil
	}
}

// newVolumeListHandler 处理 volume.list。
func newVolumeListHandler(rt runtime.Runtime) Handler {
	return func(ctx context.Context, _ []byte) (any, error) {
		vols, err := rt.ListVolumes(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]envelope.VolumeSummary, 0, len(vols))
		for _, v := range vols {
			labels := v.Labels
			if labels == nil {
				labels = map[string]string{}
			}
			out = append(out, envelope.VolumeSummary{
				Name:       v.Name,
				Driver:     v.Driver,
				Mountpoint: v.Mountpoint,
				Labels:     labels,
				Created:    v.Created,
				InUse:      v.InUse,
			})
		}
		return envelope.VolumeListResult{Volumes: out}, nil
	}
}

// newVolumeRemoveHandler 处理 volume.remove。
//
// **这是全项目最危险的一个接口**：删卷就是永久丢数据，且不可撤销。
// 因此这里做两件事：
//  1. 名称必填校验（空名字会让运行时按空前缀匹配，行为不可预测）；
//  2. 不提供 force —— Docker 本身就不支持强制删除使用中的卷，
//     我们也不该用某种绕法去实现它。
func newVolumeRemoveHandler(rt runtime.Runtime) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.VolumeRemovePayload
		if len(payload) == 0 || string(payload) == "null" {
			return nil, badRequestf("缺少载荷：需要 name")
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, badRequestf("载荷格式错误: %v", err)
		}
		name := strings.TrimSpace(p.Name)
		if name == "" {
			return nil, badRequestf("存储卷名称不能为空")
		}
		if err := rt.RemoveVolume(ctx, name); err != nil {
			return nil, err
		}
		return struct{}{}, nil
	}
}
