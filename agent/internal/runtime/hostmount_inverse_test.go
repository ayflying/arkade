package runtime

// hostmount_inverse_test.go：容器内路径 → 宿主路径的逆运算。
//
// 这份逆运算只服务于"再挂一份给 helper"的等价补偿：
// 算错会挂到别的目录，算不出来就必须跳过（ok=false），绝不能瞎猜。

import "testing"

func TestContainerPathToHost(t *testing.T) {
	e := &engine{hostRoot: "/www/server/panel/data/compose"}
	got, ok := e.containerPathToHost(DefaultHostMount + "/lanet")
	if !ok || got != "/www/server/panel/data/compose/lanet" {
		t.Fatalf("逆运算错误: %q ok=%v", got, ok)
	}
}

func TestContainerPathToHostRejectsForeignPath(t *testing.T) {
	e := &engine{hostRoot: "/data"}
	if got, ok := e.containerPathToHost("/opt/arkade/agent"); ok {
		t.Fatalf("不在挂载点下的路径不得换算: %q", got)
	}
}

func TestContainerPathToHostDisabledWithoutHostRoot(t *testing.T) {
	e := &engine{}
	if _, ok := e.containerPathToHost(DefaultHostMount + "/lanet"); ok {
		t.Fatal("未配置宿主根目录时不应换算出任何宿主路径")
	}
}

// TestComposeSourceMountsIncludesHostBind 验证容器化部署时会多挂一份宿主路径。
func TestComposeSourceMountsIncludesHostBind(t *testing.T) {
	e := &engine{hostRoot: "/www/server/panel/data/compose"}
	mounts := e.composeSourceMounts(DefaultHostMount + "/lanet")
	if len(mounts) != 2 {
		t.Fatalf("容器化部署应挂两份，实际 %d", len(mounts))
	}
	if mounts[0].Source != "/www/server/panel/data/compose/lanet" {
		t.Fatalf("第一份应为宿主路径，实际 %q", mounts[0].Source)
	}
	for _, m := range mounts {
		if !m.ReadOnly || m.Target != sourceMountPoint {
			t.Fatalf("挂载必须只读且指向固定挂载点: %+v", m)
		}
	}
}

// TestComposeSourceMountsNativeUnchanged 验证原生部署仍然只挂一份。
func TestComposeSourceMountsNativeUnchanged(t *testing.T) {
	e := &engine{}
	mounts := e.composeSourceMounts("/srv/apps/demo")
	if len(mounts) != 1 || mounts[0].Source != "/srv/apps/demo" {
		t.Fatalf("原生部署应只挂工作目录本身，实际 %+v", mounts)
	}
}
