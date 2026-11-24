package runtime

// volume.go 实现存储卷相关端点。
//
// 关键设计：InUse 字段需要知道"有没有容器在用这个卷"，而 Docker 的
// GET /volumes 不返回这个信息。做法是额外查一次 /containers/json?all=1，
// 把所有容器的 Mounts 里出现的卷名收集成一个集合，再回填到每个卷上。
//
// **不能为每个卷单独发一次请求**：卷通常有几十个，逐个查是典型的 N+1，
// 在远端引擎上会把一次列表变成几十次往返。一次容器列表 + 一次卷列表就够。

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// volumeListResp 是 GET /volumes 的响应。
type volumeListResp struct {
	Volumes []struct {
		Name       string            `json:"Name"`
		Driver     string            `json:"Driver"`
		Mountpoint string            `json:"Mountpoint"`
		Labels     map[string]string `json:"Labels"`
		CreatedAt  string            `json:"CreatedAt"` // RFC3339，可能缺失
	} `json:"Volumes"`
}

// containerMountResp 是 /containers/json?all=1 里只取 Mounts 的部分。
//
// 单独声明一个精简结构体而不是复用 listResp：listResp 已经为容器列表
// 声明了一大批字段，这里只关心挂载，混用会让"为什么容器列表要读 Mounts"
// 变得不清晰。
type containerMountResp struct {
	Names  []string `json:"Names"` // 容器的名字（可能不止一个，如 compose 的短名）
	Mounts []struct {
		Type   string `json:"Type"`
		Name   string `json:"Name"` // 命名卷才有 Name
		Source string `json:"Source"`
	} `json:"Mounts"`
}

// ListVolumes 列出存储卷，并标注哪些正在被容器使用。
func (e *engine) ListVolumes(ctx context.Context) ([]Volume, error) {
	var raw volumeListResp
	if err := e.client.getJSON(ctx, pathVolumes, &raw); err != nil {
		return nil, e.translate(err)
	}

	// 一次额外请求换取全部卷的使用情况。失败不致命：拿不到就当没有容器在用，
	// 界面显示"未使用"总比整个列表失败好（用户仍然可以看到卷并尝试删除，
	// 真被占用时删除接口会给出准确错误）。
	users, _ := e.volumeUsers(ctx)

	out := make([]Volume, 0, len(raw.Volumes))
	for _, v := range raw.Volumes {
		// 空切片而不是 nil：没人使用时也要给出「空数组」，
		// 让上层与前端不必区分 nil。
		names := users[v.Name]
		if names == nil {
			names = []string{}
		}
		out = append(out, Volume{
			Name:       v.Name,
			Driver:     v.Driver,
			Mountpoint: v.Mountpoint,
			Labels:     v.Labels,
			Created:    parseRFC3339(v.CreatedAt),
			InUse:      len(names) > 0,
			Containers: names,
		})
	}
	return out, nil
}

// volumeUsers 返回「卷名 → 使用它的容器名（已排序）」。
//
// 为什么要含已停止容器：Docker 不允许删除被"已停止但未删除"的容器引用的卷，
// 如果只统计运行中的容器，界面会显示"未使用"但删除却失败，用户会认为是 bug。
// 同时也解释了为什么「清理未使用」不能只看运行中的容器。
func (e *engine) volumeUsers(ctx context.Context) (map[string][]string, error) {
	var containers []containerMountResp
	if err := e.client.getJSON(ctx, pathContainers+"?all=1", &containers); err != nil {
		return nil, err
	}
	out := make(map[string][]string, 16)
	for _, c := range containers {
		// 一个容器可能有多个名字（Names 是数组），取第一个用于展示；
		// 全取会让同一容器在一个卷里重复出现。
		if len(c.Names) == 0 {
			continue
		}
		name := strings.TrimPrefix(c.Names[0], "/")
		for _, m := range c.Mounts {
			// 只有命名卷有 Name；bind 挂载（Type=bind）没有卷名，跳过。
			if m.Name == "" {
				continue
			}
			out[m.Name] = append(out[m.Name], name)
		}
	}
	// 排序让列表稳定：daemon 返回容器顺序不保证，不排会让同一份数据
	// 每次刷新都换一个样子。
	for _, names := range out {
		sort.Strings(names)
	}
	return out, nil
}

// RemoveVolume 删除存储卷。
//
// 这是数据丢失操作，且 Docker 不允许删除使用中的卷（返回 409）。
// 把运行时的原文透出并加上中文说明：Docker 的 "volume is in use" 已经
// 说清了原因，用户需要知道的是"先去停容器"。
func (e *engine) RemoveVolume(ctx context.Context, name string) error {
	ref := strings.TrimSpace(name)
	if ref == "" {
		return errors.New("存储卷名称不能为空")
	}
	if err := e.client.delete(ctx, pathVolumes+"/"+url.PathEscape(ref)); err != nil {
		var se *statusError
		if errors.As(err, &se) && se.Status == 409 {
			return fmt.Errorf("存储卷正被容器使用，请先停止并删除相关容器：%s", se.Body)
		}
		return translateResourceError(err, "存储卷")
	}
	return nil
}
