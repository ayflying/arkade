package runtime

// volume_test.go：存储卷列表的「谁在用」解析。
//
// 列表接口要回答「这个卷到底被哪些容器占着」——它是「清理未使用卷」
// 的唯一判断依据，也是用户判断能不能删的唯一线索。这里用真实
// /containers/json 的响应形状校验，不用 mock 结构体：
// Names 是**带前导斜杠的数组**、卷名只在 Mounts[].Name 里、
// bind 与 tmpfs 没有卷名，这些形状差异正是容易写错的地方。

import (
	"context"
	"net/http"
	"reflect"
	"testing"
)

// TestListVolumesReportsUsingContainers 验证卷到容器的映射。
func TestListVolumesReportsUsingContainers(t *testing.T) {
	const volumesBody = `{"Volumes":[
		{"Name":"db_data","Driver":"local","Mountpoint":"/v/db_data/_data"},
		{"Name":"cache","Driver":"local","Mountpoint":"/v/cache/_data"},
		{"Name":"idle","Driver":"local","Mountpoint":"/v/idle/_data"}
	]}`
	const containersBody = `[
		{"Names":["/mysql"],"Mounts":[
			{"Type":"volume","Name":"db_data","Source":"/v/db_data/_data"},
			{"Type":"bind","Source":"/srv/x","Destination":"/x"}
		]},
		{"Names":["/worker"],"Mounts":[{"Type":"volume","Name":"db_data","Source":"/v/db_data/_data"}]},
		{"Names":["/web"],"Mounts":[
			{"Type":"volume","Name":"cache","Source":"/v/cache/_data"},
			{"Type":"tmpfs","Source":"","Destination":"/tmp"}
		]}
	]`

	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == pathVolumes:
			_, _ = w.Write([]byte(volumesBody))
		case r.URL.Path == pathContainers:
			_, _ = w.Write([]byte(containersBody))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	vols, err := e.ListVolumes(context.Background())
	if err != nil {
		t.Fatalf("不应失败: %v", err)
	}
	if len(vols) != 3 {
		t.Fatalf("卷数不符: %d", len(vols))
	}
	byName := map[string]Volume{}
	for _, v := range vols {
		byName[v.Name] = v
	}

	// 多个容器用同一个卷：两个名字都要出现，且按名字排序。
	if got, want := byName["db_data"].Containers, []string{"mysql", "worker"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("db_data 的使用者不符: %v，期望 %v", got, want)
	}
	if !byName["db_data"].InUse {
		t.Fatal("db_data 应标记为使用中")
	}

	if got, want := byName["cache"].Containers, []string{"web"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cache 的使用者不符: %v，期望 %v", got, want)
	}

	// 无人使用的卷：InUse=false 且 Containers 为空（不是 nil，
	// 否则前端渲染时要到处判空）。
	idle := byName["idle"]
	if idle.InUse {
		t.Fatal("idle 不应标记为使用中")
	}
	if len(idle.Containers) != 0 {
		t.Fatalf("idle 不应有使用者: %v", idle.Containers)
	}

	// bind / tmpfs 没有卷名，绝不能把它们的 Source 当成卷名塞进来。
	for _, v := range vols {
		for _, name := range v.Containers {
			if name == "/srv/x" || name == "/tmp" {
				t.Fatalf("把挂载路径当成了容器名: %v", v.Containers)
			}
		}
	}
}

// TestListVolumesContainerNamesStripped 验证前导斜杠被去掉。
//
// daemon 给的 Names 是 "/mysql" 这种形式，直接展示会带一个斜杠，
// 看起来像路径而不是容器名。
func TestListVolumesContainerNamesStripped(t *testing.T) {
	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case pathVolumes:
			_, _ = w.Write([]byte(`{"Volumes":[{"Name":"v1","Driver":"local","Mountpoint":"/v/v1/_data"}]}`))
		case pathContainers:
			_, _ = w.Write([]byte(`[{"Names":["/plain"],"Mounts":[{"Type":"volume","Name":"v1"}]}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	vols, err := e.ListVolumes(context.Background())
	if err != nil {
		t.Fatalf("不应失败: %v", err)
	}
	if len(vols) != 1 || len(vols[0].Containers) != 1 || vols[0].Containers[0] != "plain" {
		t.Fatalf("容器名应去掉前导斜杠: %+v", vols)
	}
}

// TestListVolumesContainerLookupFailureIsNotFatal 验证查找使用者的
// 请求失败时，列表仍然返回（只是不标使用状态）。
//
// 这条守住「宁可少一列信息，也别让整页打不开」的取舍：
// 卷列表本身是主内容，使用状态是附加信息。
func TestListVolumesContainerLookupFailureIsNotFatal(t *testing.T) {
	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case pathVolumes:
			_, _ = w.Write([]byte(`{"Volumes":[{"Name":"v1","Driver":"local","Mountpoint":"/v/v1/_data"}]}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"boom"}`))
		}
	})

	vols, err := e.ListVolumes(context.Background())
	if err != nil {
		t.Fatalf("容器列表失败不应让卷列表失败: %v", err)
	}
	if len(vols) != 1 || vols[0].InUse {
		t.Fatalf("应返回未标记使用的卷: %+v", vols)
	}
}

// TestListVolumesEmptyContainersIsEmptySlice 守一条渲染约束：
// 没人使用的卷要给出空切片（不是 nil）。
//
// 前端会直接遍历这个数组，nil 与空切片在 Go 里等价，
// 但这里显式钉住「不返回 nil」的意图，避免将来有人改回 map 集合后
// 又让界面到处判 undefined。
func TestListVolumesEmptyContainersIsEmptySlice(t *testing.T) {
	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case pathVolumes:
			_, _ = w.Write([]byte(`{"Volumes":[{"Name":"v1","Driver":"local","Mountpoint":"/v/v1/_data"}]}`))
		case pathContainers:
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	vols, err := e.ListVolumes(context.Background())
	if err != nil {
		t.Fatalf("不应失败: %v", err)
	}
	if len(vols) != 1 {
		t.Fatalf("卷数不符: %d", len(vols))
	}
	if vols[0].Containers == nil {
		t.Fatal("无人使用时也应给出空切片，而不是 nil")
	}
	if len(vols[0].Containers) != 0 {
		t.Fatalf("不应有使用者: %v", vols[0].Containers)
	}
}
