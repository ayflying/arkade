package compose

// deploy_test.go：「更新部署」的判定与执行。
//
// ============================ 断言策略 ============================
//
// 分两层：
//  1. 纯函数层（解析 / 比对 / 拼文案）—— 不依赖 OS，任何平台都跑；
//  2. 端到端层 —— 用假 docker 脚本真实执行，核对**用户看到的命令序列**。
//
// 端到端层只跑 Linux（假 docker 是 shell 脚本）。
//
// 最关键的两条断言是"该什么都不做"和"该停就停"：
//   - 没有新镜像时绝不能出现 pull / up（否则用户点一次更新就白停一次服务）；
//   - 拉取失败后绝不能出现 up（否则用旧镜像重建，用户却以为已更新）。
// 这两条一旦退化，功能表面上"仍然能用"，危害却最大。

import (
	"runtime"
	"strings"
	"testing"
)

/* ───────────────────────── 1. compose 正文解析 ───────────────────────── */

// TestServiceDeclsBasic 验证服务名与镜像的对应关系。
func TestServiceDeclsBasic(t *testing.T) {
	content := `
services:
  web:
    image: nginx:1.25
    ports:
      - "80:80"
  db:
    image: postgres:16
    environment:
      POSTGRES_PASSWORD: x
volumes:
  data:
`
	decls := serviceDecls(content)
	if len(decls) != 2 {
		t.Fatalf("应解析出 2 个服务，实际 %d: %+v", len(decls), decls)
	}
	if decls[0].service != "web" || decls[0].image != "nginx:1.25" {
		t.Errorf("第一个服务解析错误: %+v", decls[0])
	}
	if decls[1].service != "db" || decls[1].image != "postgres:16" {
		t.Errorf("第二个服务解析错误: %+v", decls[1])
	}
}

// TestServiceDeclsNestedKeys 验证深层的 image（如 extends 里）不会被当成服务镜像。
//
// ============================ 为什么这条重要 ============================
//
// 深层出现 image: 说明它不是"服务直接声明的镜像"。
// 若把它当成候选，会出现"更新了一个根本没被任何服务使用的引用"，
// 用户看到"已更新"却没有任何服务变化。
func TestServiceDeclsNestedKeys(t *testing.T) {
	content := `
services:
  web:
    build:
      context: .
      args:
        BASE: alpine
    image: myapp:local
    labels:
      com.example.image: not-an-image
`
	decls := serviceDecls(content)
	if len(decls) != 1 {
		t.Fatalf("应只解析出 1 条声明，实际 %d: %+v", len(decls), decls)
	}
	if decls[0].image != "myapp:local" {
		t.Errorf("应只认服务直属的 image，实际 %q", decls[0].image)
	}
}

// TestServiceDeclsInterpolation 验证 ${VAR} 不被当成可拉取的引用。
//
// 真实引用要由 compose 结合 .env 与 shell 环境求值，Agent 里无法忠实重现；
// 拿带花括号的字符串去 pull 只会得到一条看不懂的错误。
func TestServiceDeclsInterpolation(t *testing.T) {
	content := `
services:
  web:
    image: ${REGISTRY}/app:${TAG}
`
	for _, d := range serviceDecls(content) {
		if strings.Contains(d.image, "${") {
			t.Fatalf("插值引用不应进入候选: %q", d.image)
		}
	}
	if images := serviceImages(content); len(images) != 0 {
		t.Fatalf("插值引用应被排除，实际 %v", images)
	}
}

// TestServiceDeclsQuoted 验证引号包裹的引用能取到。
func TestServiceDeclsQuoted(t *testing.T) {
	content := `
services:
  web:
    image: "ghcr.io/a/b:1.0"
`
	images := serviceImages(content)
	if len(images) != 1 || images[0] != "ghcr.io/a/b:1.0" {
		t.Fatalf("引号应被剥掉，实际 %v", images)
	}
}

// TestServiceImagesDedupKeepsOrder 验证去重但保留正文顺序。
//
// 顺序有意义：日志里"先处理哪个镜像"应当与用户写的顺序一致，便于对照。
func TestServiceImagesDedupKeepsOrder(t *testing.T) {
	content := `
services:
  a:
    image: nginx:1.25
  b:
    image: redis:7
  c:
    image: nginx:1.25
`
	images := serviceImages(content)
	if len(images) != 2 || images[0] != "nginx:1.25" || images[1] != "redis:7" {
		t.Fatalf("应去重且保持顺序，实际 %v", images)
	}
}

// TestServiceDeclsNoServicesBlock 验证没有 services 块时返回空而不是乱认。
func TestServiceDeclsNoServicesBlock(t *testing.T) {
	for _, content := range []string{"", "volumes:\n  data:\n", "services:\n"} {
		if decls := serviceDecls(content); len(decls) != 0 {
			t.Errorf("%q 不应解析出服务，实际 %+v", content, decls)
		}
	}
}

/* ───────────────────────── 2. 远端清单解析 ───────────────────────── */

// TestParseManifestIdentitySingle 验证单清单形态（取 config.digest）。
func TestParseManifestIdentitySingle(t *testing.T) {
	raw := `{
  "schemaVersion": 2,
  "config": { "digest": "sha256:abc123" }
}`
	id := parseManifestIdentity(raw)
	if id.digest != "sha256:abc123" || !id.known() {
		t.Fatalf("应解析出 config.digest，实际 %+v", id)
	}
}

// TestParseManifestIdentityList 验证清单列表：跳到嵌套 index，取一个真实清单。
func TestParseManifestIdentityList(t *testing.T) {
	raw := `{
  "manifests": [
    {
      "digest": "sha256:inner",
      "mediaType": "application/vnd.oci.image.index.v1+json",
      "platform": { "os": "linux", "architecture": "amd64" }
    },
    {
      "digest": "sha256:real",
      "mediaType": "application/vnd.oci.image.manifest.v1+json",
      "platform": { "os": "linux", "architecture": "amd64" }
    }
  ]
}`
	id := parseManifestIdentity(raw)
	if id.digest != "sha256:real" {
		t.Fatalf("应跳过嵌套 index 取真实清单，实际 %+v", id)
	}
}

// TestParseManifestIdentityOtherPlatform 验证平台不匹配时仍给出一个 digest。
//
// 不能因为"本机架构上没有"就说没有更新：镜像确实在上游变了，
// 报"已是最新"是**没有证据的结论**。
func TestParseManifestIdentityOtherPlatform(t *testing.T) {
	raw := `{"manifests":[{"digest":"sha256:arm","platform":{"os":"linux","architecture":"riscv64"}}]}`
	id := parseManifestIdentity(raw)
	if id.digest != "sha256:arm" {
		t.Fatalf("平台不匹配时应退回第一条可用清单，实际 %+v", id)
	}
}

// TestParseManifestIdentityEmpty 验证空/垃圾输入返回"未知"而不是假的 digest。
func TestParseManifestIdentityEmpty(t *testing.T) {
	for _, raw := range []string{"", "  ", "not json", "{}", `{"manifests":[]}`} {
		id := parseManifestIdentity(raw)
		if id.known() {
			t.Errorf("%q 应判为未知，实际 %+v", raw, id)
		}
	}
}

// TestManifestIdentityFallbackToImageID 验证只有镜像 ID 时也能用。
func TestManifestIdentityFallbackToImageID(t *testing.T) {
	id := parseManifestIdentity(`{"Id":"sha256:local-built"}`)
	if id.digest != "" || id.imageID != "sha256:local-built" {
		t.Fatalf("应退回镜像 ID，实际 %+v", id)
	}
	if id.identity() != "sha256:local-built" {
		t.Errorf("identity 应返回镜像 ID，实际 %q", id.identity())
	}
}

/* ───────────────────────── 3. 摘要比对 ───────────────────────── */

// TestHasRepoDigest 验证按"仓库 + digest"比对。
func TestHasRepoDigest(t *testing.T) {
	digests := []string{"nginx@sha256:aaa", "ghcr.io/a/b@sha256:bbb"}

	cases := []struct {
		ref    string
		digest string
		want   bool
		reason string
	}{
		{"nginx:1.25", "sha256:aaa", true, "同仓库同摘要"},
		{"nginx:1.25", "sha256:bash", false, "摘要不同"},
		{"ghcr.io/a/b:1.0", "sha256:bbb", true, "带路径的仓库"},
		{"other/b:1.0", "sha256:bbb", false, "摘要相同但仓库不同"},
		{"nginx:1.25", "", false, "空摘要不算命中"},
	}
	for _, c := range cases {
		if got := hasRepoDigest(digests, c.ref, c.digest); got != c.want {
			t.Errorf("%s: 期望 %v 实际 %v（%s）", c.ref, c.want, got, c.reason)
		}
	}
}

// TestRepoOf 验证仓库名提取（tag 只从最后一段剥）。
func TestRepoOf(t *testing.T) {
	cases := []struct{ in, want string }{
		{"nginx", "nginx"},
		{"nginx:1.25", "nginx"},
		{"ghcr.io/a/b:1.0", "ghcr.io/a/b"},
		{"localhost:5000/x", "localhost:5000/x"},     // 端口不是 tag
		{"localhost:5000/x:1.0", "localhost:5000/x"}, // 端口 + tag
		{"nginx@sha256:aaa", "nginx"},
		{"", ""},
	}
	for _, c := range cases {
		if got := repoOf(c.in); got != c.want {
			t.Errorf("repoOf(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// TestDedupeSorted 验证去重排序并丢弃空串。
func TestDedupeSorted(t *testing.T) {
	got := dedupeSorted([]string{"b", "a", "b", "", "  ", "c"})
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("期望 %v 实际 %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("期望 %v 实际 %v", want, got)
		}
	}
	if out := dedupeSorted(nil); len(out) != 0 {
		t.Errorf("nil 应返回空，实际 %v", out)
	}
}

/* ───────────────────────── 4. 决策辅助 ───────────────────────── */

// TestRunningLocalImages 验证"引用 → 本机镜像"的整理。
func TestRunningLocalImages(t *testing.T) {
	entries := []psEntry{
		{Image: "nginx:1.25", ImageID: "sha256:a", RepoDigests: []string{"nginx@sha256:x"}},
		{Image: "nginx:1.25", ImageID: "sha256:a"}, // 重复引用取第一条
		{Image: "", ImageID: "sha256:b"},
		{Image: "redis:7"},
	}
	got := runningLocalImages(entries)
	if len(got) != 2 {
		t.Fatalf("应得到 2 条引用，实际 %d: %+v", len(got), got)
	}
	if got["nginx:1.25"].imageID != "sha256:a" {
		t.Errorf("镜像 ID 解析错误: %+v", got["nginx:1.25"])
	}
	if len(got["nginx:1.25"].digests) != 1 {
		t.Errorf("摘要解析错误: %+v", got["nginx:1.25"])
	}
	// 旧版 compose 可能不给 ImageID/RepoDigests，此时不应崩，只留空值。
	if got["redis:7"].imageID != "" {
		t.Errorf("缺字段应为空，实际 %q", got["redis:7"].imageID)
	}
}

// TestNeedForceRecreate 验证只有"远端未知"才需要额外强制重建。
func TestNeedForceRecreate(t *testing.T) {
	if needForceRecreate(nil) {
		t.Error("空列表不需要强制重建")
	}
	known := deployHunk{ref: "a", id: manifestIdentity{digest: "sha256:1"}}
	if needForceRecreate([]deployHunk{known}) {
		t.Error("有远端身份时不需要强制重建")
	}
	// 只有镜像 ID 也算"已知"：那种情况 compose 也能识别镜像变了。
	byID := deployHunk{ref: "b", id: manifestIdentity{imageID: "sha256:2"}}
	if needForceRecreate([]deployHunk{byID}) {
		t.Error("只有镜像 ID 时不需要强制重建")
	}
	if !needForceRecreate([]deployHunk{{ref: "a"}, known}) {
		t.Error("含未知身份的条目时需要强制重建")
	}
}

// TestServicesOf 验证按镜像引用反查服务名。
func TestServicesOf(t *testing.T) {
	content := `
services:
  web:
    image: nginx:1.25
  sidecar:
    image: nginx:1.25
  db:
    image: postgres:16
`
	names := servicesOf(content, []deployHunk{{ref: "nginx:1.25"}})
	if len(names) != 2 || names[0] != "sidecar" || names[1] != "web" {
		for _, d := range serviceDecls(content) {
			t.Logf("decl service=%q image=%q", d.service, d.image)
		}
		t.Fatalf("应反查出去重排序的两个服务，实际 %v", names)
	}
	if out := servicesOf(content, nil); len(out) != 0 {
		t.Errorf("空条目应返回空，实际 %v", out)
	}
}

// TestUnknownText 验证"查不到"的说明会点名具体镜像。
//
// 用户要能判断是不是自己的私有仓库没登录；只说"部分镜像无法判定"等于没说。
func TestUnknownText(t *testing.T) {
	all := []string{"a:1", "b:1"}
	txt := unknownText([]string{"b:1"}, all)
	if !strings.Contains(txt, "b:1") {
		t.Errorf("应点名具体镜像，实际 %q", txt)
	}
	if !strings.Contains(txt, "不拉取也不重建") {
		t.Errorf("应说明不会改动，实际 %q", txt)
	}

	// 全部查不到：用"退化为按实际内容判断"的说明，而不是"已是最新"。
	allTxt := unknownText(all, all)
	if allTxt != deployAllUnknownText {
		t.Errorf("全部未知时应用统一文案，实际 %q", allTxt)
	}
	if strings.Contains(allTxt, "已是最新") {
		t.Error("查不到不能声称已是最新（那是没有证据的结论）")
	}
}

/* ───────────────────────── 5. 判定矩阵（纯函数，任何平台都跑） ───────────────────────── */

// TestPlanDeployWith 穷举"要不要更新"的每一种情形。
//
// ============================ 这张表为什么必须在任何平台都能跑 ============================
//
// 判成"有更新"就会白拉一遍并重启容器；判成"没更新"就会让用户以为已是最新。
// 两种错误都不可见（功能表面上仍然工作），因此必须靠穷举测试守住，
// 而不是只靠"在 Linux 上跑一遍假 docker"。
func TestPlanDeployWith(t *testing.T) {
	// 远端固定返回"新版本"，除了明确指定的情形。
	remoteDigest := func(string) manifestIdentity {
		return manifestIdentity{digest: "sha256:new"}
	}

	cases := []struct {
		name        string
		images      []string
		running     map[string]localImage
		inspect     func(string) manifestIdentity
		wantHunks   []string // 需要拉取/重建的引用
		wantUnknown []string // 无法判定的引用
		reason      string
	}{
		{
			name:    "本机已是最新 → 不动",
			images:  []string{"nginx:1"},
			running: map[string]localImage{"nginx:1": {imageID: "sha256:c", digests: []string{"nginx@sha256:new"}}},
			inspect: remoteDigest,
			// 摘要一致就不该出现在待拉取列表里
			reason: "镜像没变时绝不能重建容器",
		},
		{
			name:      "本机摘要过期 → 需要更新",
			images:    []string{"nginx:1"},
			running:   map[string]localImage{"nginx:1": {imageID: "sha256:c", digests: []string{"nginx@sha256:old"}}},
			inspect:   remoteDigest,
			wantHunks: []string{"nginx:1"},
		},
		{
			name:      "本机没有该容器 → 需要更新",
			images:    []string{"nginx:1"},
			running:   map[string]localImage{},
			inspect:   remoteDigest,
			wantHunks: []string{"nginx:1"},
			reason:    "首次部署也走这条路径，交给 up 创建",
		},
		{
			name:        "远端查不到 → 无法判定，不进待拉取列表",
			images:      []string{"nginx:1"},
			running:     map[string]localImage{"nginx:1": {imageID: "sha256:c"}},
			inspect:     func(string) manifestIdentity { return manifestIdentity{} },
			wantUnknown: []string{"nginx:1"},
			reason:      "查不到就说查不到，不能猜",
		},
		{
			name:   "只有镜像 ID 且一致 → 不动",
			images: []string{"app:local"},
			running: map[string]localImage{
				"app:local": {imageID: "sha256:same"},
			},
			inspect: func(string) manifestIdentity { return manifestIdentity{imageID: "sha256:same"} },
			reason:  "本地构建的镜像没有仓库摘要，只能靠镜像 ID 比对",
		},
		{
			name:   "只有镜像 ID 且不同 → 需要更新",
			images: []string{"app:local"},
			running: map[string]localImage{
				"app:local": {imageID: "sha256:old"},
			},
			inspect:   func(string) manifestIdentity { return manifestIdentity{imageID: "sha256:new"} },
			wantHunks: []string{"app:local"},
		},
		{
			name:   "多个镜像混着判定",
			images: []string{"a:1", "b:1", "c:1"},
			running: map[string]localImage{
				"a:1": {imageID: "sha256:a", digests: []string{"a@sha256:new"}}, // 最新
				"b:1": {imageID: "sha256:b", digests: []string{"b@sha256:old"}}, // 过期
				// c:1 没有容器
			},
			inspect:   remoteDigest,
			wantHunks: []string{"b:1", "c:1"},
			reason:    "只处理真正需要更新的那些",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hunks, unknown := planDeployWith(c.images, c.running, c.inspect)

			gotRefs := make([]string, 0, len(hunks))
			for _, h := range hunks {
				gotRefs = append(gotRefs, h.ref)
			}
			if strings.Join(gotRefs, ",") != strings.Join(c.wantHunks, ",") {
				t.Errorf("待更新引用应为 %v，实际 %v（%s）", c.wantHunks, gotRefs, c.reason)
			}
			if strings.Join(unknown, ",") != strings.Join(c.wantUnknown, ",") {
				t.Errorf("无法判定应为 %v，实际 %v", c.wantUnknown, unknown)
			}
		})
	}
}

// TestChangedAfterPull 验证"拉取后内容有没有变"的判定。
//
// 拉取成功不等于有新内容：镜像仓库可能重打了同一个标签。
// 若这里判错，就会出现"每次都重建"（白停服务）或"从不重建"（镜像新、容器旧）。
func TestChangedAfterPull(t *testing.T) {
	cases := []struct {
		name    string
		hunk    deployHunk
		local   localImage
		current string
		want    bool
		reason  string
	}{
		{
			name:   "远端摘要已在本机 → 没变",
			hunk:   deployHunk{ref: "nginx:1", id: manifestIdentity{digest: "sha256:new"}},
			local:  localImage{imageID: "sha256:c", digests: []string{"nginx@sha256:new"}},
			want:   false,
			reason: "拉到的是同一份内容，重建一次纯属浪费",
		},
		{
			name:   "远端摘要不在本机 → 变了",
			hunk:   deployHunk{ref: "nginx:1", id: manifestIdentity{digest: "sha256:new"}},
			local:  localImage{imageID: "sha256:c", digests: []string{"nginx@sha256:old"}},
			want:   true,
			reason: "不重建就会让镜像新、容器旧",
		},
		{
			name:    "远端未知且引用已指向新 ID → 变了",
			hunk:    deployHunk{ref: "app:local"},
			local:   localImage{imageID: "sha256:old"},
			current: "sha256:new",
			want:    true,
		},
		{
			name:    "远端未知且引用仍指向旧 ID → 没变",
			hunk:    deployHunk{ref: "app:local"},
			local:   localImage{imageID: "sha256:old"},
			current: "sha256:old",
			want:    false,
		},
		{
			name:    "远端未知且查不到引用 → 按变了处理",
			hunk:    deployHunk{ref: "app:local"},
			local:   localImage{imageID: "sha256:old"},
			current: "",
			want:    true,
			reason:  "宁可多重建一次，也不要落成半更新状态",
		},
		{
			name:  "远端只给镜像 ID 且不同 → 变了",
			hunk:  deployHunk{ref: "app:local", id: manifestIdentity{imageID: "sha256:new"}},
			local: localImage{imageID: "sha256:old"},
			want:  true,
		},
		{
			name:  "远端只给镜像 ID 且相同 → 没变",
			hunk:  deployHunk{ref: "app:local", id: manifestIdentity{imageID: "sha256:old"}},
			local: localImage{imageID: "sha256:old"},
			want:  false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := changedAfterPull(c.hunk, c.local, c.current); got != c.want {
				t.Errorf("期望 %v 实际 %v（%s）", c.want, got, c.reason)
			}
		})
	}
}

/* ───────────────────────── 6. 端到端（假 docker，仅 Linux） ───────────────────────── */

// deployScript 生成假 docker：按 argv 分派，并可控制每条命令的结果。
//
// ============================ 为什么要按"引用"给不同答案 ============================
//
// 真实环境里 `docker manifest inspect` 是**按引用**返回的，不可能所有引用都给同一个
// digest；`docker images <ref>` 也是按引用查的。假 docker 若对所有引用都给同一个答案，
// 就会造出一种现实中不存在的状态：多个服务"同时都过期"，
// 于是"只更新真正变了的那个"这条断言永远测不出来（假阳性通过）。
//
// 因此远端摘要与本机镜像 ID 都支持按引用（仓库名）分别配置，用 marker 表实现：
// 先在 JSON 里放一个标记串，再在脚本里按引用名做替换。
// 这样既不用额外依赖（jq），也不用为每个用例写一份脚本。
//
// 参数约定：
//   - remote  ：「能按平台匹配出 digest」时用，键是仓库名（repoOf 的结果）；
//     没配的仓库返回查不到（模拟私有/离线）。
//   - localIDs：「查本机某引用当前指向哪份内容」时用（docker images <ref>）。
//   - pullExit：pull 的退出码。
func deployScript(psJSON string, remote, localIDs map[string]string, pullExit int) string {
	script := `
if [ "$1" = "manifest" ]; then
  ref="$3"
  case "$ref" in
` + manifestCases(remote, "sha256:__REMOTE_") + `
  esac
  exit 1
fi
if [ "$1" = "images" ]; then
  ref="$4"
  case "$ref" in
` + manifestCases(localIDs, "__LOCAL_") + `
  esac
  exit 0
fi
if [ "$1" = "compose" ]; then
  case "$*" in
    *" ps "*)
      printf '%s' '` + psJSON + `'
      exit 0
      ;;
    *" pull "*)
      while [ "$1" != "pull" ]; do shift; done
      shift
      if [ "$1" = "--" ]; then shift; fi
      [ "$#" -gt 0 ] || exit 1
      for service in "$@"; do
        case "$service" in
          web|db) ;;
          *) echo "no such service: $service" >&2; exit 1 ;;
        esac
      done
      echo "pulling..."
      exit ` + deployExitCode(pullExit) + `
      ;;
    *" up "*)
      echo "up: $*"
      exit 0
      ;;
  esac
  exit 0
fi
exit 0
`
	// 最后把标记替换成真实内容：标记里带仓库名，替换后就是该引用自己的答案。
	for repo, digest := range remote {
		script = strings.ReplaceAll(script, "sha256:__REMOTE_"+repo, digest)
	}
	for repo, id := range localIDs {
		script = strings.ReplaceAll(script, "__LOCAL_"+repo, id)
	}
	return script
}

// manifestCases 生成"按引用分派"的 shell case 分支。
//
// 单清单形态：config.digest 就是比对目标，因此直接把它设成该引用的远端摘要。
// 两个分支（单清单 / 清单列表）都给，避免"只有一种形态被测到"。
func manifestCases(table map[string]string, marker string) string {
	var b strings.Builder
	for repo := range table {
		b.WriteString("    *")
		b.WriteString(repo)
		b.WriteString("*)\n")
		if strings.HasPrefix(marker, "sha256:") {
			// manifest：给单清单形态，digest 由最后一轮替换填真值。
			b.WriteString(`      printf '{"config":{"digest":"` + marker + repo + `"}}'` + "\n")
		} else {
			b.WriteString(`      printf '%s' '` + marker + repo + `'` + "\n")
		}
		b.WriteString("      exit 0\n      ;;\n")
	}
	return b.String()
}

// itoa 避免为一行格式化引入 strconv 之外的东西（保持测试自解释）。
func deployExitCode(n int) string {
	if n == 0 {
		return "0"
	}
	return "1"
}

// startDeploy 在给定 compose 正文上跑一次 deploy，返回终态快照。
func startDeploy(t *testing.T, content, script, reqID string) PullSnapshot {
	t.Helper()
	_, p := newPullTestManager(t, writeScript(t, script))
	if _, _, err := p.mgr.ws.CommitProject("app", content, ""); err != nil {
		t.Fatal(err)
	}
	snap, err := p.StartOperation(OperationSpec{Action: ActionDeploy}, "app", reqID)
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	return waitTerminal(t, p, snap.TaskID)
}

// composeContent 是一份两服务的正文（web 用一个镜像，db 用另一个）。
const composeContent = `
services:
  web:
    image: nginx:1.25
  db:
    image: postgres:16
`

// TestDeployNoUpdateDoesNothing 验证没有新镜像时**一条写命令都不跑**。
//
// ============================ 这是本功能的核心承诺 ============================
//
// 用户点「更新部署」时服务正在跑。若实现照旧 pull + up，
// 就算镜像没变也会把容器重建一遍——短暂中断、丢失未持久化状态，
// 而用户以为"只是检查一下有没有更新"。
func TestDeployNoUpdateDoesNothing(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}

	// 两个服务的本机摘要都已经等于远端摘要。
	ps := `[{"Service":"web","State":"running","Image":"nginx:1.25","ImageID":"sha256:w",
	         "RepoDigests":["nginx@sha256:same"]},
	        {"Service":"db","State":"running","Image":"postgres:16","ImageID":"sha256:d",
	         "RepoDigests":["postgres@sha256:same"]}]`
	remote := map[string]string{"nginx:1.25": "sha256:same", "postgres:16": "sha256:same"}

	final := startDeploy(t, composeContent, deployScript(ps, remote, nil, 0), "deploy-noop-1")

	if final.Status != PullSucceeded {
		t.Fatalf("应成功（无更新也是成功），实际 %s: %s", final.Status, final.Error)
	}
	if len(final.UpdatedServices) != 0 {
		t.Fatalf("不应更新任何服务，实际 %v", final.UpdatedServices)
	}
	for _, cmd := range cmdEvents(final) {
		// 探测命令不会作为 command 事件出现（probe 不带观察者），
		// 因此这里出现任何命令都说明跑了写操作。
		if strings.Contains(cmd, "pull") || strings.Contains(cmd, " up ") || strings.HasSuffix(cmd, " up") {
			t.Fatalf("没有更新时不应执行写命令，实际出现 %q", cmd)
		}
	}
	notes := strings.Join(streamEvents(final, "stage"), "\n")
	if !strings.Contains(notes, "没有可更新的镜像") {
		t.Fatalf("应明确告知没有可更新的镜像，实际说明 %q", notes)
	}
}

// TestDeployPullsOnlyChangedService 验证只拉有更新的那个镜像、只重建对应服务。
func TestDeployPullsOnlyChangedService(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}

	// web 的摘要已过期，db 已是最新（远端就给出本机已有的那个摘要）。
	ps := `[{"Service":"web","State":"running","Image":"nginx:1.25","ImageID":"sha256:w",
	         "RepoDigests":["nginx@sha256:old"]},
	        {"Service":"db","State":"running","Image":"postgres:16","ImageID":"sha256:d",
	         "RepoDigests":["postgres@sha256:same"]}]`
	remote := map[string]string{"nginx:1.25": "sha256:new", "postgres:16": "sha256:same"}

	final := startDeploy(t, composeContent, deployScript(ps, remote, nil, 0), "deploy-up-1")

	if final.Status != PullSucceeded {
		t.Fatalf("应成功，实际 %s: %s", final.Status, final.Error)
	}
	if len(final.UpdatedServices) != 1 || final.UpdatedServices[0] != "web" {
		t.Fatalf("应只更新 web，实际 %v", final.UpdatedServices)
	}

	var pulls, ups []string
	for _, cmd := range cmdEvents(final) {
		tail := composeTail(cmd)
		if strings.Contains(tail, " pull ") {
			pulls = append(pulls, tail)
		}
		if strings.Contains(tail, " up ") {
			ups = append(ups, tail)
		}
	}
	if len(pulls) != 1 || !strings.HasSuffix(pulls[0], " pull -- web") {
		t.Fatalf("应只按服务名拉 web，实际 %v", pulls)
	}
	if len(ups) != 1 || !strings.Contains(ups[0], "web") {
		t.Fatalf("应只重建 web，实际 %v", ups)
	}
	if strings.Contains(ups[0], "db") {
		t.Fatalf("不应重建没有更新的服务，实际 %q", ups[0])
	}
}

// TestDeployPullFailureStopsBeforeUp 验证拉取失败后绝不继续重建。
//
// ============================ 这是安全断言 ============================
//
// "拉取失败却用旧镜像重建"会让服务白停一次，而用户以为已经更新到新版。
func TestDeployPullFailureStopsBeforeUp(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}

	ps := `[{"Service":"web","State":"running","Image":"nginx:1.25","ImageID":"sha256:w",
	         "RepoDigests":["nginx@sha256:old"]},
	        {"Service":"db","State":"running","Image":"postgres:16","ImageID":"sha256:d",
	         "RepoDigests":["postgres@sha256:same"]}]`
	remote := map[string]string{"nginx:1.25": "sha256:new", "postgres:16": "sha256:same"}

	final := startDeploy(t, composeContent, deployScript(ps, remote, nil, 1), "deploy-pullfail-1")

	if final.Status != PullFailed {
		t.Fatalf("拉取失败时任务应为 failed，实际 %s", final.Status)
	}
	if !strings.Contains(final.Error, "拉取镜像") {
		t.Fatalf("错误应说明是拉取失败，实际 %q", final.Error)
	}
	if !strings.Contains(final.Error, "没有改动") {
		t.Fatalf("错误应说明容器未改动，实际 %q", final.Error)
	}
	if len(final.UpdatedServices) != 0 {
		t.Fatalf("失败时不应声称更新了服务，实际 %v", final.UpdatedServices)
	}
	for _, cmd := range cmdEvents(final) {
		if strings.Contains(composeTail(cmd), " up ") {
			t.Fatalf("拉取失败后绝不能重建，实际出现 %q", cmd)
		}
	}
}

// TestDeployUnknownManifestDegradesToPull 验证全部查不到远端时退化为"按实际内容判断"。
//
// 私有仓库未登录、离线环境里查不到摘要是常态。此时既不能说"已是最新"
// （没有证据），也不能直接拒绝（功能就不可用了）。
func TestDeployUnknownManifestDegradesToPull(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}

	ps := `[{"Service":"web","State":"running","Image":"nginx:1.25","ImageID":"sha256:w",
	         "RepoDigests":["nginx@sha256:local"]}]`
	// remote 传 nil：manifest inspect 一律非零退出，模拟"查不到清单"
	// （私有仓库未登录 / 离线 / 镜像已删除）。
	// images 给出"拉取后该引用已指向新内容"，退化路径据此判定需要重建。
	localIDs := map[string]string{"nginx:1.25": "sha256:afterpull"}

	final := startDeploy(t, "services:\n  web:\n    image: nginx:1.25\n",
		deployScript(ps, nil, localIDs, 0), "deploy-unknown-1")

	if final.Status != PullSucceeded {
		t.Fatalf("退化为拉取后应成功，实际 %s: %s", final.Status, final.Error)
	}
	notes := strings.Join(streamEvents(final, "stage"), "\n")
	if !strings.HasPrefix(notes, "正在检查本机容器和镜像仓库是否有更新") ||
		strings.Count(notes, "无法在拉取前判断") != 1 {
		t.Fatalf("应先进入检查阶段，再提示一次退化路径，实际 %q", notes)
	}

	var sawPull, sawUp bool
	for _, cmd := range cmdEvents(final) {
		tail := composeTail(cmd)
		if strings.Contains(tail, " pull ") {
			sawPull = true
		}
		if strings.Contains(tail, " up ") {
			sawUp = true
		}
	}
	if !sawPull {
		t.Fatal("退化路径应执行拉取")
	}
	if !sawUp {
		t.Fatal("拉到了新内容时应重建（否则镜像新、容器旧）")
	}
}

// TestDeployRequiresServices 验证没有 image 声明时给出明确结论而不是空跑。
func TestDeployRequiresServices(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}

	final := startDeploy(t, "services:\n  web:\n    build: .\n",
		deployScript("[]", nil, nil, 0), "deploy-noimage-1")

	if final.Status != PullFailed {
		t.Fatalf("没有镜像声明应失败，实际 %s", final.Status)
	}
	if !strings.Contains(final.Error, "image") {
		t.Fatalf("错误应指出缺少 image 声明，实际 %q", final.Error)
	}
}
