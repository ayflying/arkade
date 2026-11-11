package runtime

// hostpath_test.go：宿主路径换算的边界。
//
// 这组用例保护的是"读错项目"这类事故：
// 换算过宽会把 Agent 带去看别的目录，换算过窄会让本来就通的路径被误报为读不到。

import "testing"

func TestHostPathDisabledWithoutHostRoot(t *testing.T) {
	e := &engine{}
	got, ok := e.hostPath("/www/server/panel/data/compose/lanet")
	if !ok || got != "/www/server/panel/data/compose/lanet" {
		t.Fatalf("未配置宿主根目录时应原样返回，实际 %q ok=%v", got, ok)
	}
}

func TestHostPathRewritesWithinRoot(t *testing.T) {
	e := &engine{hostRoot: "/www/server/panel/data/compose"}
	got, ok := e.hostPath("/www/server/panel/data/compose/lanet")
	if !ok {
		t.Fatal("位于宿主根目录之下的路径应可换算")
	}
	if got != DefaultHostMount+"/lanet" {
		t.Fatalf("换算结果错误: %q", got)
	}
}

func TestHostPathRejectsOutsideRoot(t *testing.T) {
	e := &engine{hostRoot: "/www/server/panel/data/compose"}
	for _, p := range []string{
		"/www/server/panel/data/other",
		"/www/server/panel/data/compose-evil",
		"/etc/passwd",
	} {
		if got, ok := e.hostPath(p); ok {
			t.Fatalf("根目录之外的路径不得换算: %q → %q", p, got)
		}
	}
}

func TestHostPathRejectsDotDot(t *testing.T) {
	e := &engine{hostRoot: "/data"}
	if got, ok := e.hostPath("/data/../etc"); ok {
		t.Fatalf("含 .. 的路径必须拒绝，实际换算成 %q", got)
	}
}

func TestHostPathCustomMount(t *testing.T) {
	e := &engine{hostRoot: "/data", hostMount: "/mnt/host"}
	got, ok := e.hostPath("/data/a/b")
	if !ok || got != "/mnt/host/a/b" {
		t.Fatalf("自定义挂载点换算错误: %q ok=%v", got, ok)
	}
}
