package runtime

// container_networks_test.go：列表接口的网络映射。
//
// ============================ 为什么用真实 JSON ============================
//
// /containers/json 的 NetworkSettings 是 NetworkSettingsSummary，
// **只有 Networks 一个字段**（与 inspect 的 NetworkSettings 不同，
// 后者才有 Ports）。这个差异最容易写错：
// 把列表响应当成 inspect 解析，Ports 就永远解析不到；
// 反过来在列表里找 EndpointID / Gateway 也找不到。
//
// 因此这里用接近 daemon 原文的列表 JSON 做断言。

import (
	"context"
	"net/http"
	"testing"
)

// listEngine 返回给定列表 JSON 的假引擎。
func listEngine(t *testing.T, body string) *engine {
	t.Helper()
	return newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/containers/json" {
			_, _ = w.Write([]byte(body))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
}

// realListJSON 是贴近 daemon 的列表响应：
//   - 一个有双栈网络（IPv4 + IPv6）；
//   - 一个 host 模式（Networks 为空 map）；
//   - 一个没有任何 NetworkSettings 字段（老引擎/Podman）。
const realListJSON = `[
  {
    "Id": "aaa111",
    "Names": ["/demo-web-1"],
    "Image": "nginx:alpine",
    "Created": 1762000000,
    "State": "running",
    "Status": "Up 2 hours",
    "Labels": {"com.docker.compose.project": "demo"},
    "Ports": [{"IP":"0.0.0.0","PrivatePort":80,"PublicPort":8080,"Type":"tcp"}],
    "HostConfig": {"NetworkMode": "demo_default"},
    "NetworkSettings": {
      "Networks": {
        "demo_default": {"IPAddress": "172.18.0.9", "GlobalIPv6Address": "fd00::9"},
        "bridge": {"IPAddress": "172.17.0.5", "GlobalIPv6Address": ""}
      }
    }
  },
  {
    "Id": "bbb222",
    "Names": ["/hostmode"],
    "Image": "alpine",
    "Created": 1762000001,
    "State": "running",
    "Status": "Up 1 hour",
    "Labels": {},
    "HostConfig": {"NetworkMode": "host"},
    "NetworkSettings": {"Networks": {}}
  },
  {
    "Id": "ccc333",
    "Names": ["/legacy"],
    "Image": "alpine",
    "Created": 1762000002,
    "State": "exited",
    "Status": "Exited (0) 1 hour ago",
    "Labels": {}
  }
]`

// TestListMapsNetworksFromRealResponse 验证网络与地址被正确映射。
func TestListMapsNetworksFromRealResponse(t *testing.T) {
	e := listEngine(t, realListJSON)
	cs, err := e.List(context.Background(), true)
	if err != nil {
		t.Fatalf("不应失败: %v", err)
	}
	if len(cs) != 3 {
		t.Fatalf("应有 3 个容器，实际 %d", len(cs))
	}

	web := cs[0]
	if len(web.Networks) != 2 {
		t.Fatalf("web 应有 2 个网络，实际 %+v", web.Networks)
	}
	// 稳定排序：bridge 在 demo_default 前。
	if web.Networks[0].Name != "bridge" || web.Networks[1].Name != "demo_default" {
		t.Fatalf("网络未按名字排序: %+v", web.Networks)
	}
	if web.Networks[0].IPAddress != "172.17.0.5" {
		t.Fatalf("bridge IPv4 不符: %+v", web.Networks[0])
	}
	if web.Networks[0].IPv6Address != "" {
		t.Fatalf("bridge 无 IPv6，应为空: %+v", web.Networks[0])
	}
	if web.Networks[1].IPAddress != "172.18.0.9" || web.Networks[1].IPv6Address != "fd00::9" {
		t.Fatalf("双栈网络地址不符: %+v", web.Networks[1])
	}
	// NetworkMode 也要带出来。
	if web.NetworkMode != "demo_default" {
		t.Fatalf("NetworkMode 不符: %q", web.NetworkMode)
	}
}

// TestListHostModeHasNoFakeIP 验证 host 模式**不伪造**任何地址。
//
// host 模式下容器与宿主共享网络栈，本来就没有独立 IP。
// 返回 0.0.0.0 或宿主机地址会让用户以为容器有可访问地址——那是编造事实。
func TestListHostModeHasNoFakeIP(t *testing.T) {
	e := listEngine(t, realListJSON)
	cs, err := e.List(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	host := cs[1]
	if host.NetworkMode != "host" {
		t.Fatalf("NetworkMode 应为 host，实际 %q", host.NetworkMode)
	}
	if len(host.Networks) != 0 {
		t.Fatalf("host 模式不应有网络条目，实际 %+v", host.Networks)
	}
}

// TestListMissingNetworkSettingsIsSafe 验证整个 NetworkSettings 缺失时不 panic。
func TestListMissingNetworkSettingsIsSafe(t *testing.T) {
	e := listEngine(t, realListJSON)
	cs, err := e.List(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	legacy := cs[2]
	if len(legacy.Networks) != 0 {
		t.Fatalf("无 NetworkSettings 时不应有网络，实际 %+v", legacy.Networks)
	}
	if legacy.NetworkMode != "" {
		t.Fatalf("无 HostConfig 时 NetworkMode 应为空，实际 %q", legacy.NetworkMode)
	}
}

// TestListNetworksAreStable 验证多次列表的网络顺序一致。
func TestListNetworksAreStable(t *testing.T) {
	e := listEngine(t, realListJSON)
	var first string
	for i := 0; i < 20; i++ {
		cs, err := e.List(context.Background(), true)
		if err != nil {
			t.Fatal(err)
		}
		order := ""
		for _, n := range cs[0].Networks {
			order += n.Name + "|"
		}
		if first == "" {
			first = order
			continue
		}
		if order != first {
			t.Fatalf("第 %d 次顺序变了：%q ≠ %q", i, order, first)
		}
	}
}
