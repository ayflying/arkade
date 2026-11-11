package runtime

// compose_source_hostpath_test.go：容器化部署下"宿主路径要换算"的解析结果。
//
// 这两条用例守住一个用户可见的分歧：
//   - 配了宿主根目录：解析出的挂载目录必须是容器内路径（否则 helper 挂不到）；
//   - 没挂载：必须给出"没挂进来"的明确原因，而不是通用的读取失败。

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestResolveComposeSourceMapsHostPath(t *testing.T) {
	e := composeLabelsServer(t, []map[string]any{
		composeContainer("lanet", "/www/server/panel/data/compose/lanet",
			"/www/server/panel/data/compose/lanet/docker-compose.yaml"),
	})
	e.hostRoot = "/www/server/panel/data/compose"

	wd, rel, err := e.resolveComposeSource(context.Background(), "lanet")
	if err != nil {
		t.Fatalf("不应失败: %v", err)
	}
	if wd != DefaultHostMount+"/lanet" {
		t.Fatalf("挂载目录应是容器内路径，实际 %q", wd)
	}
	if rel != "docker-compose.yaml" {
		t.Fatalf("相对路径不符: %q", rel)
	}
}

func TestResolveComposeSourceReportsUnmountedHostDir(t *testing.T) {
	e := composeLabelsServer(t, []map[string]any{
		composeContainer("lanet", "/www/server/panel/data/compose/lanet",
			"/www/server/panel/data/compose/lanet/docker-compose.yaml"),
	})
	// 配了别的宿主根目录：该项目的目录不在其中，等于没挂进来。
	e.hostRoot = "/srv/other"

	_, _, err := e.resolveComposeSource(context.Background(), "lanet")
	if err == nil {
		t.Fatal("宿主目录没挂进来时必须给出明确原因")
	}
	var blocked *SourceBlockerError
	if !errors.As(err, &blocked) {
		t.Fatalf("期望 SourceBlockerError，实际 %T: %v", err, err)
	}
	if !strings.Contains(blocked.Reason, "没有挂载进 Agent 容器") {
		t.Fatalf("原因应说明没挂载，实际: %s", blocked.Reason)
	}
	// 原因里出现宿主路径是刻意的：用户要据此去改挂载配置。
	if !strings.Contains(blocked.Reason, "/www/server/panel/data/compose/lanet") {
		t.Fatalf("原因应带上宿主目录，实际: %s", blocked.Reason)
	}
}
