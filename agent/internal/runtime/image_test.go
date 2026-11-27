package runtime

import (
	"context"
	"net/http"
	"slices"
	"testing"
)

func TestListImagesAssociatesByIDNotTag(t *testing.T) {
	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case pathImages:
			_, _ = w.Write([]byte(`[
			{"Id":"sha256:new","RepoTags":["app:latest"]},
			{"Id":"sha256:old","RepoTags":["<none>:<none>"],"RepoDigests":["app@sha256:manifest"]},
			{"Id":"sha256:idle","RepoTags":["<none>:<none>"]}
			]`))
		case pathContainers:
			if r.URL.Query().Get("all") != "1" {
				t.Error("必须包含停止的容器")
			}
			_, _ = w.Write([]byte(`[
			{"Names":["/worker"],"Image":"app:latest","ImageID":"sha256:old","State":"exited"},
			{"Names":["/web"],"Image":"app:latest","ImageID":"old","State":"running"}
			]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	images, err := e.ListImages(context.Background())
	if err != nil || len(images) != 3 {
		t.Fatalf("读取镜像失败：%v，数量 %d", err, len(images))
	}
	if got := images[0]; !slices.Equal(got.Tags, []string{"app:latest"}) || got.ContainerNames == nil || len(got.ContainerNames) != 0 || got.Dangling {
		t.Fatalf("带标签未引用的镜像识别错误：%+v", got)
	}
	if got := images[1]; !slices.Equal(got.Repositories, []string{"app"}) || !slices.Equal(got.ContainerNames, []string{"web", "worker"}) || got.Dangling {
		t.Fatalf("旧镜像的仓库或引用识别错误：%+v", got)
	}
	if got := images[2]; !got.Dangling || got.ContainerNames == nil {
		t.Fatalf("悬空镜像识别错误：%+v", got)
	}
}

func TestListImagesUnknownUsageIsNotDangling(t *testing.T) {
	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == pathImages {
			_, _ = w.Write([]byte(`[{"Id":"sha256:old"}]`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	images, err := e.ListImages(context.Background())
	if err != nil || len(images) != 1 || images[0].ContainerNames != nil || images[0].Dangling {
		t.Fatalf("无法确认引用时不能视为可清理：%+v，%v", images, err)
	}
}
