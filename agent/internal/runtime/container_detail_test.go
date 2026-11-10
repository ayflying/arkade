package runtime

// container_detail_test.go：用**真实 daemon 响应形状**验证 inspect 解析。
//
// ============================ 为什么不用 mock ============================
//
// inspect 的坑全在解析：Ports 是 map[string][]{HostIp,HostPort}、
// Mounts 用 RW 布尔（我们的 InspectMount 用的是取反的 ReadOnly）、
// PidsLimit 可能为 null（未设上限）而不是 0。
// 用手写的假结构体会把这些**形状差异**隐藏掉，测出来的是假象。
// 因此这里放入接近 daemon 原文的 JSON。

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// realInspectJSON 是接近 docker inspect 原文的响应（已剔除我们不需要的字段，
// 但保留了会踩坑的那些：Env 故意留着，用来验证我们**没有**把它带出来）。
const realInspectJSON = `{
  "Id": "0123456789abcdef0123456789abcdef",
  "Name": "/demo-web-1",
  "Created": "2026-11-10T02:03:04.123456789Z",
  "Image": "sha256:aaaaaaaaaaaaaaaa",
  "State": {
    "Status": "running",
    "Running": true,
    "ExitCode": 0,
    "StartedAt": "2026-11-10T02:03:05.000000000Z",
    "FinishedAt": "0001-01-01T00:00:00Z"
  },
  "RestartCount": 3,
  "Config": {
    "Image": "nginx:alpine",
    "Entrypoint": ["/entry.sh"],
    "Cmd": ["-g", "daemon off;"],
    "Env": ["SECRET_TOKEN=SHOULD-NOT-LEAK", "DB_PASS=also-secret"],
    "ExposedPorts": {"80/tcp": {}, "443/tcp": {}, "9000/tcp": {}}
  },
  "HostConfig": {
    "NetworkMode": "bridge",
    "Memory": 536870912,
    "NanoCpus": 1500000000,
    "PidsLimit": 128
  },
  "NetworkSettings": {
    "Ports": {
      "80/tcp": [{"HostIp": "0.0.0.0", "HostPort": "8080"}, {"HostIp": "::", "HostPort": "8080"}],
      "443/tcp": null,
      "9000/tcp": [{"HostIp": "127.0.0.1", "HostPort": ""}]
    },
    "Networks": {
      "bridge": {"IPAddress": "172.17.0.5"},
      "demo_default": {"IPAddress": "172.18.0.9"}
    }
  },
  "Mounts": [
    {"Type": "bind", "Source": "/srv/demo/data", "Destination": "/data", "RW": true},
    {"Type": "volume", "Source": "demo_cache", "Destination": "/cache", "RW": false}
  ],
  "Secrets": [{"File": "/run/secrets/x"}]
}`

// inspectEngine 构造返回给定 inspect JSON 的假引擎。
func inspectEngine(t *testing.T, body string) *engine {
	t.Helper()
	return newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/json") {
			_, _ = w.Write([]byte(body))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
}

// TestInspectExposedFromNetworkSettingsOnly 验证 spec 为 null 但
// Config.ExposedPorts **缺失**时，仍能收集到仅暴露端口。
//
// 这是真实漏报场景：老引擎只给 NetworkSettings.Ports:{"80/tcp":null}，
// 不给 Config.ExposedPorts。只看后者就会把这个端口整个丢掉，
// 用户完全看不到容器声明了 80。
func TestInspectExposedFromNetworkSettingsOnly(t *testing.T) {
	const body = `{
	  "Id":"x","Name":"/c",
	  "Config":{"Image":"a"},
	  "NetworkSettings":{"Ports":{"80/tcp":null,"443/tcp":[]}}
	}`
	e := inspectEngine(t, body)
	d, err := e.InspectContainer(context.Background(), "c")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(d.ExposedPorts, ",")
	if !strings.Contains(got, "80/tcp") {
		t.Fatalf("80/tcp（spec:null）应进仅暴露，实际 %v", d.ExposedPorts)
	}
	if !strings.Contains(got, "443/tcp") {
		t.Fatalf("443/tcp（空数组）应进仅暴露，实际 %v", d.ExposedPorts)
	}
	if len(d.Ports) != 0 {
		t.Fatalf("不应产生已发布条目: %+v", d.Ports)
	}
}

// TestInspectExposedMergedAndDeduped 验证两个来源合并去重。
//
// 同一个端口同时出现在两处（很常见），必须只出现一次；
// 且 "80" 与 "80/tcp" 两种写法要归一化成同一个 key。
func TestInspectExposedMergedAndDeduped(t *testing.T) {
	const body = `{
	  "Id":"x","Name":"/c",
	  "Config":{"Image":"a","ExposedPorts":{"80/tcp":{},"80":{},"53/udp":{}}},
	  "NetworkSettings":{"Ports":{"80/tcp":null,"443/tcp":null}}
	}`
	e := inspectEngine(t, body)
	d, err := e.InspectContainer(context.Background(), "c")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, s := range d.ExposedPorts {
		seen[s]++
	}
	for spec, n := range seen {
		if n != 1 {
			t.Fatalf("%s 重复出现 %d 次: %v", spec, n, d.ExposedPorts)
		}
	}
	want := map[string]bool{"80/tcp": true, "53/udp": true, "443/tcp": true}
	if len(d.ExposedPorts) != len(want) {
		t.Fatalf("应有 %d 个仅暴露端口，实际 %v", len(want), d.ExposedPorts)
	}
	for spec := range want {
		if seen[spec] != 1 {
			t.Fatalf("缺少 %s: %v", spec, d.ExposedPorts)
		}
	}
}

// TestInspectInvalidSpecNeverAppendsZeroPort 验证容器端口无法解析时
// **不**产生 Private=0 的假映射。
//
// 旧实现的 bug：splitPortSpec 失败返回 0 却仍然 append，
// 前端会显示"宿主 8080 → 容器 0"这种不存在的映射。
func TestInspectInvalidSpecNeverAppendsZeroPort(t *testing.T) {
	const body = `{
	  "Id":"x","Name":"/c",
	  "Config":{"Image":"a","ExposedPorts":{"bogus":{},"0/tcp":{}}},
	  "NetworkSettings":{"Ports":{
	    "bogus":[{"HostIp":"0.0.0.0","HostPort":"8080"}],
	    "0/tcp":[{"HostIp":"0.0.0.0","HostPort":"8081"}]
	  }}
	}`
	e := inspectEngine(t, body)
	d, err := e.InspectContainer(context.Background(), "c")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Ports) != 0 {
		t.Fatalf("非法 spec 不得产生端口映射，实际 %+v", d.Ports)
	}
	for _, p := range d.Ports {
		if p.Private == 0 {
			t.Fatalf("禁止出现容器端口 0: %+v", p)
		}
	}
	// 不可归一化的 spec 也不进仅暴露。
	for _, s := range d.ExposedPorts {
		if strings.HasPrefix(s, "bogus") || strings.HasPrefix(s, "0/") {
			t.Fatalf("非法 spec 不应出现在仅暴露里: %v", d.ExposedPorts)
		}
	}
}

// TestInspectPortSortStableAcrossIPFamily 验证同端口双栈绑定的顺序稳定。
//
// 0.0.0.0 与 :: 的容器端口/协议/宿主端口三层完全相同，
// 缺 IP 这一级会让顺序随 map 遍历抖动。
func TestInspectPortSortStableAcrossIPFamily(t *testing.T) {
	const body = `{
	  "Id":"x","Name":"/c",
	  "Config":{"Image":"a"},
	  "NetworkSettings":{"Ports":{
	    "80/tcp":[
	      {"HostIp":"::","HostPort":"8080"},
	      {"HostIp":"0.0.0.0","HostPort":"8080"}
	    ]
	  }}
	}`
	e := inspectEngine(t, body)
	// 跑多次，结果必须完全一致且 IPv4 在 IPv6 前（字符串序 "0" < ":"）。
	var first string
	for i := 0; i < 20; i++ {
		d, err := e.InspectContainer(context.Background(), "c")
		if err != nil {
			t.Fatal(err)
		}
		if len(d.Ports) != 2 {
			t.Fatalf("应有 2 条绑定，实际 %+v", d.Ports)
		}
		order := d.Ports[0].IP + "|" + d.Ports[1].IP
		if first == "" {
			first = order
			if d.Ports[0].IP != "0.0.0.0" {
				t.Fatalf("排序应以宿主 IP 为末级（0.0.0.0 在前），实际 %s", order)
			}
			continue
		}
		if order != first {
			t.Fatalf("排序不稳定：第 %d 次 %s ≠ %s", i, order, first)
		}
	}
}

// TestInspectPublishedSpecNotInExposed 验证已发布的端口不会重复出现在仅暴露里。
func TestInspectPublishedSpecNotInExposed(t *testing.T) {
	const body = `{
	  "Id":"x","Name":"/c",
	  "Config":{"Image":"a","ExposedPorts":{"80/tcp":{}}},
	  "NetworkSettings":{"Ports":{
	    "80/tcp":[{"HostIp":"0.0.0.0","HostPort":"8080"}]
	  }}
	}`
	e := inspectEngine(t, body)
	d, err := e.InspectContainer(context.Background(), "c")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Ports) != 1 {
		t.Fatalf("应有 1 条已发布: %+v", d.Ports)
	}
	if len(d.ExposedPorts) != 0 {
		t.Fatalf("已发布端口不应进仅暴露: %v", d.ExposedPorts)
	}
}

// TestInspectNetworkIncludesIPv6 验证 inspect 也带出全局 IPv6 地址。
//
// 与列表保持一致，Hub 才能用同一套结构渲染"网络名 + IPv4 + IPv6"。
func TestInspectNetworkIncludesIPv6(t *testing.T) {
	const body = `{
	  "Id":"x","Name":"/c","Config":{"Image":"a"},
	  "NetworkSettings":{
	    "Networks":{
	      "dual":{"IPAddress":"172.18.0.9","GlobalIPv6Address":"fd00::9"},
	      "bridge":{"IPAddress":"172.17.0.5","GlobalIPv6Address":""}
	    }
	  }
	}`
	e := inspectEngine(t, body)
	d, err := e.InspectContainer(context.Background(), "c")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]InspectNetwork{}
	for _, n := range d.Networks {
		byName[n.Name] = n
	}
	if n := byName["dual"]; n.IPAddress != "172.18.0.9" || n.IPv6Address != "fd00::9" {
		t.Fatalf("双栈网络解析错误: %+v", n)
	}
	if n := byName["bridge"]; n.IPv6Address != "" {
		t.Fatalf("无 IPv6 时应为空: %+v", n)
	}
}

// TestInspectHostModeHasNoFakeIP 验证 host 模式不伪造地址。
func TestInspectHostModeHasNoFakeIP(t *testing.T) {
	const body = `{
	  "Id":"x","Name":"/c","Config":{"Image":"a"},
	  "HostConfig":{"NetworkMode":"host"},
	  "NetworkSettings":{"Networks":{}}
	}`
	e := inspectEngine(t, body)
	d, err := e.InspectContainer(context.Background(), "c")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Networks) != 0 {
		t.Fatalf("host 模式不应有网络条目: %+v", d.Networks)
	}
	if d.NetworkMode != "host" {
		t.Fatalf("NetworkMode 应为 host: %q", d.NetworkMode)
	}
}

// TestInspectParsesRealDaemonShape 验证真实形状能被正确解析。
func TestInspectParsesRealDaemonShape(t *testing.T) {
	e := inspectEngine(t, realInspectJSON)
	d, err := e.InspectContainer(context.Background(), "demo-web-1")
	if err != nil {
		t.Fatalf("不应失败: %v", err)
	}

	if d.Name != "demo-web-1" {
		t.Fatalf("名字应去掉前导斜杠，实际 %q", d.Name)
	}
	if d.Image != "nginx:alpine" {
		t.Fatalf("镜像应为 Config.Image，实际 %q", d.Image)
	}
	if d.State != "running" || d.RestartCount != 3 {
		t.Fatalf("状态/重启次数不符: %q %d", d.State, d.RestartCount)
	}
	if d.NetworkMode != "bridge" {
		t.Fatalf("网络模式不符: %q", d.NetworkMode)
	}
	// 命令 = Entrypoint + Cmd。
	wantCmd := []string{"/entry.sh", "-g", "daemon off;"}
	if len(d.Command) != len(wantCmd) {
		t.Fatalf("命令不符: %v", d.Command)
	}
	for i := range wantCmd {
		if d.Command[i] != wantCmd[i] {
			t.Fatalf("命令第 %d 项不符: %v", i, d.Command)
		}
	}

	// 已发布端口：80/tcp 有两条绑定（0.0.0.0 与 ::），都算发布。
	pub := d.Ports
	if len(pub) != 2 {
		t.Fatalf("应有 2 条已发布端口，实际 %d: %+v", len(pub), pub)
	}
	if pub[0].Private != 80 || pub[0].Public != 8080 || pub[0].Type != "tcp" {
		t.Fatalf("端口映射解析错误: %+v", pub[0])
	}

	// 仅暴露未发布：443/tcp（null 绑定）与 9000/tcp（HostPort 为空）。
	exposed := strings.Join(d.ExposedPorts, ",")
	if !strings.Contains(exposed, "443/tcp") {
		t.Fatalf("443/tcp 应属于仅暴露，实际 %v", d.ExposedPorts)
	}
	if !strings.Contains(exposed, "9000/tcp") {
		t.Fatalf("9000/tcp（宿主端口为空）应属于仅暴露，实际 %v", d.ExposedPorts)
	}
	if strings.Contains(exposed, "80/tcp") {
		t.Fatalf("80/tcp 已发布，不该出现在仅暴露里: %v", d.ExposedPorts)
	}

	// 挂载：RW=true → ReadOnly=false；RW=false → ReadOnly=true。
	if len(d.Mounts) != 2 {
		t.Fatalf("挂载数不符: %d", len(d.Mounts))
	}
	byDst := map[string]InspectMount{}
	for _, m := range d.Mounts {
		byDst[m.Destination] = m
	}
	if m := byDst["/data"]; m.Type != "bind" || m.ReadOnly || m.Source != "/srv/demo/data" {
		t.Fatalf("bind 挂载解析错误: %+v", m)
	}
	if m := byDst["/cache"]; m.Type != "volume" || !m.ReadOnly {
		t.Fatalf("只读卷挂载解析错误（RW=false 应得 ReadOnly=true）: %+v", m)
	}

	// 网络：按名字排序，带地址。
	if len(d.Networks) != 2 {
		t.Fatalf("网络数不符: %d", len(d.Networks))
	}
	if d.Networks[0].Name != "bridge" && d.Networks[0].Name != "demo_default" {
		t.Fatalf("网络名异常: %+v", d.Networks)
	}
	// 排序保证稳定。
	if d.Networks[0].Name > d.Networks[1].Name {
		t.Fatalf("网络未排序: %+v", d.Networks)
	}

	// 资源上限。
	if d.MemoryBytes != 536870912 || d.NanoCPUs != 1500000000 {
		t.Fatalf("资源上限不符: %d %d", d.MemoryBytes, d.NanoCPUs)
	}
	if d.PidsLimit == nil || *d.PidsLimit != 128 {
		t.Fatalf("pids 上限不符: %v", d.PidsLimit)
	}
}

// TestInspectDoesNotLeakEnv 验证 Env / Secrets **没有**被带出来。
//
// 这是本方法最重要的安全断言：窄视图里根本没有 Env 字段，
// 序列化整份结果也不该出现密钥值。
func TestInspectDoesNotLeakEnv(t *testing.T) {
	e := inspectEngine(t, realInspectJSON)
	d, err := e.InspectContainer(context.Background(), "demo-web-1")
	if err != nil {
		t.Fatal(err)
	}
	raw := mustJSON(t, d)
	for _, secret := range []string{"SHOULD-NOT-LEAK", "DB_PASS", "SECRET_TOKEN", "run/secrets"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("详情不应包含环境变量/密钥 %q: %s", secret, raw)
		}
	}
}

// TestInspectPidsLimitNullOrMissing 验证未设上限时 PidsLimit 为 nil（不是 0）。
func TestInspectPidsLimitNullOrMissing(t *testing.T) {
	const body = `{"Id":"x","Name":"/c","Config":{"Image":"a"},"HostConfig":{"PidsLimit":null}}`
	e := inspectEngine(t, body)
	d, err := e.InspectContainer(context.Background(), "c")
	if err != nil {
		t.Fatal(err)
	}
	if d.PidsLimit != nil {
		t.Fatalf("未设上限应为 nil，实际 %v", *d.PidsLimit)
	}
}

// TestInspectInvalidHostPortDropped 验证异常宿主端口被丢弃而不是回绕。
func TestInspectInvalidHostPortDropped(t *testing.T) {
	const body = `{
	  "Id":"x","Name":"/c","Config":{"Image":"a"},
	  "NetworkSettings":{"Ports":{
	    "80/tcp":[{"HostIp":"0.0.0.0","HostPort":"99999"}],
	    "81/tcp":[{"HostIp":"0.0.0.0","HostPort":"notanumber"}],
	    "82/tcp":[{"HostIp":"0.0.0.0","HostPort":"0"}]
	  }}
	}`
	e := inspectEngine(t, body)
	d, err := e.InspectContainer(context.Background(), "c")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Ports) != 0 {
		t.Fatalf("非法宿主端口应全部丢弃，实际 %+v", d.Ports)
	}
}

// TestInspectRejectsEmptyRef 验证空 ID 在发起请求前就被拒。
func TestInspectRejectsEmptyRef(t *testing.T) {
	e := inspectEngine(t, realInspectJSON)
	if _, err := e.InspectContainer(context.Background(), ""); err == nil {
		t.Fatal("空容器 ID 必须被拒绝")
	}
}
