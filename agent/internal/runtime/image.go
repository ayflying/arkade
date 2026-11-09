package runtime

// image.go 实现镜像相关端点。
//
// 只做列表与删除两件事。不提供"拉取镜像"——镜像拉取是编排动作的一部分
// （compose pull 会按 compose 文件里的引用去拉），单独暴露一个自由
// 拉取任意镜像的接口会扩大攻击面而收益很低。

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
	Created     int64             `json:"Created"`
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
		Created:  r.Created,
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
