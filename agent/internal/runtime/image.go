package runtime

// image.go 实现镜像的**列表与删除**端点。
//
// 关于"拉取/导出/导入"：这三件事不在这里，而在 image_transfer_engine.go。
// 分文件而不是塞进本文件，是因为它们的形态完全不同——
// 本文件都是"小 JSON 一问一答"，而传输是**流**：
// 拉取是 NDJSON 进度流、导出是多 GB 的 tar 流、导入是接收 tar 流。
// 它们必须走 client 的不缓冲路径（openStream/postStream/openUpload），
// 绝不能走会把响应体读进内存的 do()。
//
// 这里曾经写过一句"不提供拉取镜像，因为收益低"——那是**错的**判断，
// 已废弃。用户明确需要跨机的镜像迁移，拉取是其中第一环。
// 真正的边界不在"要不要做"，而在怎么安全地做：只支持公共仓库、
// 不收凭据、流式不缓冲、有硬性磁盘限额（见 image_transfer_runtime.go）。

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// imageListResp 是 GET /images/json 的响应元素。
//
// Docker 返回的字段是 PascalCase；只声明用得到的。
type imageListResp struct {
	ID          string            `json:"Id"`
	RepoTags    []string          `json:"RepoTags"`
	RepoDigests []string          `json:"RepoDigests"`
	Size        int64             `json:"Size"`
	Created     flexUnixTime      `json:"Created"` // 整数或 RFC3339 串，见 parse_time.go
	Labels      map[string]string `json:"Labels"`
}

// ListImages 列出镜像。
//
// 全部返回（不加 ?all= 以外参数）：界面需要看到悬空镜像，
// 因为它们是磁盘占用的大户，用户清理磁盘时恰恰要找这些。
func (e *engine) ListImages(ctx context.Context) ([]Image, error) {
	var raw []imageListResp
	if err := e.client.getJSON(ctx, pathImages, &raw); err != nil {
		return nil, e.translate(err)
	}
	out := make([]Image, 0, len(raw))
	for _, r := range raw {
		out = append(out, toImage(r))
	}
	return out, nil
}

// toImage 把运行时结构转成抽象层 Image。
//
// RepoTags 为空或仅含 <none>:<none> 时视为悬空镜像。
// Docker 用 "<none>:<none>" 表示无标签，直接透给前端会让界面出现一片
// 莫名其妙的 "<none>:<none>" 条目。
func toImage(r imageListResp) Image {
	tags := make([]string, 0, len(r.RepoTags))
	for _, t := range r.RepoTags {
		if strings.HasPrefix(t, "<none>") {
			continue
		}
		tags = append(tags, t)
	}
	return Image{
		ID:       r.ID,
		Tags:     tags,
		Size:     r.Size,
		Created:  r.Created.unix(),
		Dangling: len(tags) == 0,
	}
}

// RemoveImage 删除镜像。
//
// force 语义与 docker rmi -f 一致。不 force 时若镜像被容器引用，
// Docker 返回 409，这里翻译成中文提示让用户知道要用 force。
func (e *engine) RemoveImage(ctx context.Context, imageID string, force bool) error {
	ref := strings.TrimSpace(imageID)
	if ref == "" {
		return errors.New("镜像 ID 不能为空")
	}
	path := pathImages + "/" + url.PathEscape(ref)
	if force {
		path += "?force=1"
	}
	if err := e.client.delete(ctx, path); err != nil {
		return translateResourceError(err, "镜像")
	}
	return nil
}

// translateResourceError 把资源类端点的错误翻译成可读中文。
//
// 与 engine.translate 的区别：那个只处理 404（容器场景）。
// 资源删除的错误语义更丰富——409 通常表示"被占用"，值得单独给出可操作提示。
func translateResourceError(err error, kind string) error {
	if err == nil {
		return nil
	}
	var se *statusError
	if !errors.As(err, &se) {
		return err
	}
	switch {
	case se.Status == 404:
		return fmt.Errorf("%w: %s不存在", ErrNotFound, kind)
	case se.Status == 409:
		// 占用冲突：把运行时原文附上，它通常已经说明了原因
		// （"image is being used by running container"）。
		return fmt.Errorf("%s正被使用，无法删除（如需强制删除请使用强制选项）：%s", kind, se.Body)
	default:
		return err
	}
}
